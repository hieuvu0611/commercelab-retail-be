package timeline

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection is the MongoDB collection of shipment timelines.
const Collection = "fulfillment_timelines"

// MongoStore keeps one document per shipment: {shipment_id, order_id, entries: [...]}.
type MongoStore struct{ coll *mongo.Collection }

// NewMongoStore binds the store and makes sure the unique shipment id index exists (same key as the database
// init script, so creating it again is harmless).
func NewMongoStore(ctx context.Context, store *db.MongoStore) (*MongoStore, error) {
	coll := store.Database().Collection(Collection)
	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "shipment_id", Value: 1}}, Options: options.Index().SetUnique(true)})
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	return &MongoStore{coll: coll}, nil
}

func (m *MongoStore) Append(ctx context.Context, shipmentID, orderID string, e Entry) error {
	_, err := m.coll.UpdateOne(ctx, bson.D{{Key: "shipment_id", Value: shipmentID}},
		bson.D{{Key: "$setOnInsert", Value: bson.D{{Key: "order_id", Value: orderID}}}, {Key: "$push", Value: bson.D{{Key: "entries", Value: e}}}},
		options.UpdateOne().SetUpsert(true))
	return dependency.Wrap(err)
}

func (m *MongoStore) Get(ctx context.Context, shipmentID string) ([]Entry, error) {
	var doc struct {
		Entries []Entry `bson:"entries"`
	}
	err := m.coll.FindOne(ctx, bson.D{{Key: "shipment_id", Value: shipmentID}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	return doc.Entries, nil
}
