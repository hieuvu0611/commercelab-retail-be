package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	// mongoServerSelectionTimeout bounds how long an operation waits for a reachable server,
	// so an unreachable Mongo fails in seconds instead of hanging a request.
	mongoServerSelectionTimeout = 2 * time.Second
	// mongoMaxPoolSize keeps the driver small enough for the 250 MB service containers.
	mongoMaxPoolSize = 5
	// defaultMongoDatabase is used when no database name is given.
	defaultMongoDatabase = "commerce_lab_doc"
)

// MongoStore is a thin wrapper over the official MongoDB driver bound to one database.
// Authentication comes from the connection URI (for example "?authSource=admin").
type MongoStore struct {
	client *mongo.Client
	db     *mongo.Database
}

// NewMongoStore prepares a store for uri without contacting the server (the driver connects lazily).
func NewMongoStore(uri, dbName string) (*MongoStore, error) {
	if uri == "" {
		return nil, errors.New("MONGO_URI is empty")
	}
	if dbName == "" {
		dbName = defaultMongoDatabase
	}
	client, err := mongo.Connect(options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(mongoServerSelectionTimeout).
		SetMaxPoolSize(mongoMaxPoolSize).
		// Decode embedded documents as bson.M so callers can type-assert them like top-level documents.
		SetBSONOptions(&options.BSONOptions{DefaultDocumentM: true}))
	if err != nil {
		return nil, fmt.Errorf("invalid MONGO_URI: %w", err)
	}
	return &MongoStore{client: client, db: client.Database(dbName)}, nil
}

// ConnectMongo connects to Mongo, verifies the credentials with a ping (retrying while the server starts)
// and returns the store. The store is returned twice to keep the signature existing callers use.
func ConnectMongo(ctx context.Context, uri, dbName string) (*MongoStore, *MongoStore, error) {
	store, err := NewMongoStore(uri, dbName)
	if err != nil {
		return nil, nil, err
	}
	if err := retryStartup(ctx, startupAttempts, startupRetryDelay, func() error { return store.Ping(ctx) }); err != nil {
		_ = store.Close(context.Background())
		return nil, nil, err
	}
	return store, store, nil
}

// Ping checks that the server is reachable and the credentials are accepted.
func (m *MongoStore) Ping(ctx context.Context) error {
	return m.db.RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Err()
}

// FindAll returns every document of collection matching filter, iterating all cursor batches.
func (m *MongoStore) FindAll(ctx context.Context, collection string, filter bson.D) ([]bson.M, error) {
	cursor, err := m.db.Collection(collection).Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("find in %s: %w", collection, err)
	}
	var docs []bson.M
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("read cursor of %s: %w", collection, err)
	}
	return docs, nil
}

// Close releases the driver's connections.
func (m *MongoStore) Close(ctx context.Context) error {
	return m.client.Disconnect(ctx)
}
