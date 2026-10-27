package db

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const opMsgCode int32 = 2013

var mongoReqID int32 = 1

// MongoStore connects to MongoDB commerce_lab_doc using lightweight BSON v2 + OP_MSG wire protocol
// to minimize memory footprint under the 250MB RAM container cap.
type MongoStore struct {
	addr   string
	dbName string
}

func ConnectMongo(ctx context.Context, uri, dbName string) (*MongoStore, *MongoStore, error) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Host == "" {
		return nil, nil, errors.New("invalid MONGO_URI")
	}
	if dbName == "" {
		dbName = "commerce_lab_doc"
	}
	store := &MongoStore{
		addr:   parsed.Host,
		dbName: dbName,
	}
	if err := store.Ping(ctx); err != nil {
		return nil, nil, err
	}
	return store, store, nil
}

func (m *MongoStore) Ping(ctx context.Context) error {
	_, err := m.RunCommand(ctx, bson.D{
		{Key: "ping", Value: 1},
		{Key: "$db", Value: m.dbName},
	})
	return err
}

func (m *MongoStore) InsertOne(ctx context.Context, collection string, doc any) error {
	_, err := m.RunCommand(ctx, bson.D{
		{Key: "insert", Value: collection},
		{Key: "documents", Value: bson.A{doc}},
		{Key: "$db", Value: m.dbName},
	})
	return err
}

func (m *MongoStore) FindAll(ctx context.Context, collection string, filter bson.D) ([]bson.M, error) {
	raw, err := m.RunCommand(ctx, bson.D{
		{Key: "find", Value: collection},
		{Key: "filter", Value: filter},
		{Key: "$db", Value: m.dbName},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     float64 `bson:"ok"`
		Cursor struct {
			FirstBatch []bson.M `bson:"firstBatch"`
		} `bson:"cursor"`
	}
	if err := bson.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.OK != 1 {
		return nil, fmt.Errorf("failed to query MongoDB collection %s", collection)
	}
	return resp.Cursor.FirstBatch, nil
}

func (m *MongoStore) RunCommand(ctx context.Context, cmd bson.D) ([]byte, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", m.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	body, err := bson.Marshal(cmd)
	if err != nil {
		return nil, err
	}

	// OP_MSG layout: 16-byte MsgHeader + 4-byte flagBits (0) + 1-byte section kind (0) + BSON document
	totalLen := int32(16 + 4 + 1 + len(body))
	reqID := atomic.AddInt32(&mongoReqID, 1)

	buf := make([]byte, 21+len(body))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(totalLen))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(reqID))
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(opMsgCode))
	binary.LittleEndian.PutUint32(buf[16:20], 0) // flagBits
	buf[20] = 0                                  // payloadType 0 (single BSON body)
	copy(buf[21:], body)

	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}

	var hdr [16]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	respLen := int(binary.LittleEndian.Uint32(hdr[0:4]))
	if respLen < 21 || respLen > 16*1024*1024 {
		return nil, errors.New("invalid OP_MSG response from MongoDB")
	}
	payload := make([]byte, respLen-16)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}
	return payload[5:], nil
}
