package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	skuCollection   = "catalog_sku_specs"
	draftCollection = "catalog_drafts"
)

// MongoProductStore implements domain.ProductStore on MongoDB.
type MongoProductStore struct {
	products *mongo.Collection
	skus     *mongo.Collection
	drafts   *mongo.Collection
}

// NewMongoProductStore creates the store and ensures its indexes (unique slug, SKU by product).
func NewMongoProductStore(ctx context.Context, store *db.MongoStore) (*MongoProductStore, error) {
	d := store.Database()
	s := &MongoProductStore{
		products: d.Collection(productCollection), skus: d.Collection(skuCollection), drafts: d.Collection(draftCollection),
	}
	if _, err := s.products.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uniq_slug"),
	}); err != nil {
		return nil, fmt.Errorf("create product slug index: %w", dependency.Wrap(err))
	}
	if _, err := s.skus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "productId", Value: 1}}, Options: options.Index().SetName("by_product"),
	}); err != nil {
		return nil, fmt.Errorf("create sku product index: %w", dependency.Wrap(err))
	}
	return s, nil
}

func (s *MongoProductStore) InsertProduct(ctx context.Context, p domain.Product) error {
	if _, err := s.products.InsertOne(ctx, p); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrProductSlugTaken
		}
		return dependency.Wrap(err)
	}
	return nil
}

func (s *MongoProductStore) GetProduct(ctx context.Context, id string) (domain.Product, error) {
	var p domain.Product
	err := s.products.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Product{}, domain.ErrProductNotFound
	}
	if err != nil {
		return domain.Product{}, dependency.Wrap(err)
	}
	return p, nil
}

func (s *MongoProductStore) SaveProduct(ctx context.Context, p domain.Product, expectedVersion int64) error {
	res, err := s.products.ReplaceOne(ctx, bson.D{{Key: "_id", Value: p.ID}, {Key: "version", Value: expectedVersion}}, p)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrProductSlugTaken
		}
		return dependency.Wrap(err)
	}
	if res.MatchedCount == 1 {
		return nil
	}
	if _, err := s.GetProduct(ctx, p.ID); err != nil {
		return err // not found or store failure
	}
	return domain.ErrVersionConflict
}

// ListProducts matches Q against name and slug case-insensitively; Q is escaped, so it is plain text.
func (s *MongoProductStore) ListProducts(ctx context.Context, f domain.ProductFilter) ([]domain.Product, int64, error) {
	filter := bson.D{}
	if f.Status != "" {
		filter = append(filter, bson.E{Key: "status", Value: string(f.Status)})
	}
	if f.Q != "" {
		re := bson.D{{Key: "$regex", Value: regexp.QuoteMeta(f.Q)}, {Key: "$options", Value: "i"}}
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{bson.D{{Key: "name", Value: re}}, bson.D{{Key: "slug", Value: re}}}})
	}
	total, err := s.products.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	cur, err := s.products.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: 1}}).
		SetSkip(int64((f.Page-1)*f.Limit)).SetLimit(int64(f.Limit)))
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	out := []domain.Product{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	return out, total, nil
}

func (s *MongoProductStore) GetDraft(ctx context.Context, productID string) (domain.ProductRevision, bool, error) {
	var d domain.ProductRevision
	err := s.drafts.FindOne(ctx, bson.D{{Key: "_id", Value: productID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.ProductRevision{}, false, nil
	}
	if err != nil {
		return domain.ProductRevision{}, false, dependency.Wrap(err)
	}
	return d, true, nil
}

func (s *MongoProductStore) SaveDraft(ctx context.Context, d domain.ProductRevision, expectedVersion int64) error {
	if expectedVersion == 0 {
		if _, err := s.drafts.InsertOne(ctx, d); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return domain.ErrVersionConflict
			}
			return dependency.Wrap(err)
		}
		return nil
	}
	res, err := s.drafts.ReplaceOne(ctx, bson.D{{Key: "_id", Value: d.ProductID}, {Key: "product.version", Value: expectedVersion}}, d)
	if err != nil {
		return dependency.Wrap(err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (s *MongoProductStore) ListSKUs(ctx context.Context, productID string) ([]domain.SKU, error) {
	cur, err := s.skus.Find(ctx, bson.D{{Key: "productId", Value: productID}}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	out := []domain.SKU{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, dependency.Wrap(err)
	}
	return out, nil
}

func (s *MongoProductStore) GetSKU(ctx context.Context, skuID string) (domain.SKU, error) {
	var sku domain.SKU
	err := s.skus.FindOne(ctx, bson.D{{Key: "_id", Value: skuID}}).Decode(&sku)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.SKU{}, domain.ErrSKUNotFound
	}
	if err != nil {
		return domain.SKU{}, dependency.Wrap(err)
	}
	return sku, nil
}

func (s *MongoProductStore) InsertSKU(ctx context.Context, sku domain.SKU) error {
	if _, err := s.skus.InsertOne(ctx, sku); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrSKUCodeTaken
		}
		return dependency.Wrap(err)
	}
	return nil
}

func (s *MongoProductStore) SaveSKU(ctx context.Context, sku domain.SKU) error {
	res, err := s.skus.ReplaceOne(ctx, bson.D{{Key: "_id", Value: sku.SkuID}}, sku)
	if err != nil {
		return dependency.Wrap(err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrSKUNotFound
	}
	return nil
}

func (s *MongoProductStore) DeleteSKU(ctx context.Context, skuID string) error {
	_, err := s.skus.DeleteOne(ctx, bson.D{{Key: "_id", Value: skuID}})
	return dependency.Wrap(err)
}

var _ domain.ProductStore = (*MongoProductStore)(nil)
