package snapshot

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection is the MongoDB collection of order snapshots.
const Collection = "order_commercial_snapshots"

// MongoStore keeps snapshots in MongoDB, one document per order id.
type MongoStore struct{ coll *mongo.Collection }

// NewMongoStore binds the store and makes sure the unique order id index exists (it has the same name the
// database init script gives it).
func NewMongoStore(ctx context.Context, store *db.MongoStore) (*MongoStore, error) {
	coll := store.Database().Collection(Collection)
	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "order_id", Value: 1}}, Options: options.Index().SetUnique(true)})
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	return &MongoStore{coll: coll}, nil
}

func (m *MongoStore) Save(ctx context.Context, s Snapshot) error {
	_, err := m.coll.UpdateOne(ctx, bson.D{{Key: "order_id", Value: s.OrderID}}, bson.D{{Key: "$setOnInsert", Value: s}},
		options.UpdateOne().SetUpsert(true))
	return dependency.Wrap(err)
}

func (m *MongoStore) Get(ctx context.Context, orderID string) (Snapshot, bool, error) {
	var s Snapshot
	err := m.coll.FindOne(ctx, bson.D{{Key: "order_id", Value: orderID}}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, dependency.Wrap(err)
	}
	return s, true, nil
}
