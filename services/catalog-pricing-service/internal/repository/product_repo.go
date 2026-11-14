package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	if _, err := s.products.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "name", Value: "text"}, {Key: "brand", Value: "text"}, {Key: "description", Value: "text"}},
		Options: options.Index().SetName("text_search").SetWeights(bson.D{{Key: "name", Value: 5}, {Key: "brand", Value: 2}, {Key: "description", Value: 1}}),
	}); err != nil {
		return nil, fmt.Errorf("create product text index: %w", dependency.Wrap(err))
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

func (s *MongoProductStore) DeleteDraft(ctx context.Context, productID string) error {
	_, err := s.drafts.DeleteOne(ctx, bson.D{{Key: "_id", Value: productID}})
	return dependency.Wrap(err)
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

// NewMongoStorefrontStore returns the read side of the catalog on an existing Mongo connection. It creates no
// indexes (NewMongoProductStore does).
func NewMongoStorefrontStore(store *db.MongoStore) *MongoProductStore {
	d := store.Database()
	return &MongoProductStore{products: d.Collection(productCollection), skus: d.Collection(skuCollection), drafts: d.Collection(draftCollection)}
}

const maxStorefrontScan = 5001

// FindPublished implements domain.StorefrontStore. The search text goes to Mongo's text index as a word search,
// never into a regular expression.
func (s *MongoProductStore) FindPublished(ctx context.Context, text string, categoryIDs []string) ([]domain.PublishedRef, error) {
	filter := bson.D{{Key: "status", Value: string(domain.ProductPublished)}}
	if categoryIDs != nil {
		filter = append(filter, bson.E{Key: "categoryIds", Value: bson.D{{Key: "$in", Value: categoryIDs}}})
	}
	if text != "" {
		filter = append(filter, bson.E{Key: "$text", Value: bson.D{{Key: "$search", Value: text}}})
	}
	cur, err := s.products.Find(ctx, filter, options.Find().
		SetProjection(bson.D{{Key: "_id", Value: 1}, {Key: "publishedAt", Value: 1}}).SetLimit(maxStorefrontScan))
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	var rows []struct {
		ID          string     `bson:"_id"`
		PublishedAt *time.Time `bson:"publishedAt"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, dependency.Wrap(err)
	}
	refs := make([]domain.PublishedRef, len(rows))
	for i, r := range rows {
		refs[i] = domain.PublishedRef{ID: r.ID}
		if r.PublishedAt != nil {
			refs[i].PublishedAt = *r.PublishedAt
		}
	}
	return refs, nil
}

// ActiveSKUs implements domain.StorefrontStore; attribute values are matched as escaped, anchored text.
func (s *MongoProductStore) ActiveSKUs(ctx context.Context, productIDs []string, attrs map[string]string) ([]domain.SKU, error) {
	filter := bson.D{{Key: "productId", Value: bson.D{{Key: "$in", Value: productIDs}}}, {Key: "isActive", Value: true}}
	for axis, value := range attrs {
		filter = append(filter, bson.E{Key: "attributes." + axis, Value: bson.D{
			{Key: "$regex", Value: "^" + regexp.QuoteMeta(strings.TrimSpace(value)) + "$"}, {Key: "$options", Value: "i"},
		}})
	}
	cur, err := s.skus.Find(ctx, filter)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	out := []domain.SKU{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, dependency.Wrap(err)
	}
	return out, nil
}

// PublishedByIDs implements domain.StorefrontStore.
func (s *MongoProductStore) PublishedByIDs(ctx context.Context, ids []string) ([]domain.Product, error) {
	cur, err := s.products.Find(ctx, bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "status", Value: string(domain.ProductPublished)},
	})
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	var found []domain.Product
	if err := cur.All(ctx, &found); err != nil {
		return nil, dependency.Wrap(err)
	}
	byID := make(map[string]domain.Product, len(found))
	for _, p := range found {
		byID[p.ID] = p
	}
	out := make([]domain.Product, 0, len(found))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// PublishedBySlug implements domain.StorefrontStore.
func (s *MongoProductStore) PublishedBySlug(ctx context.Context, slug string) (domain.Product, error) {
	var p domain.Product
	err := s.products.FindOne(ctx, bson.D{{Key: "slug", Value: slug}, {Key: "status", Value: string(domain.ProductPublished)}}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Product{}, domain.ErrProductNotFound
	}
	if err != nil {
		return domain.Product{}, dependency.Wrap(err)
	}
	return p, nil
}

var _ domain.StorefrontStore = (*MongoProductStore)(nil)

// Upsert writes the product document as given, replacing any existing one with the same id. It bypasses the
// version check and the workflow and exists for the demo seed only.
func (s *MongoProductStore) Upsert(ctx context.Context, p domain.Product) error {
	_, err := s.products.ReplaceOne(ctx, bson.D{{Key: "_id", Value: p.ID}}, p, options.Replace().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return domain.ErrProductSlugTaken
	}
	return dependency.Wrap(err)
}

// UpsertSKU writes the SKU document as given (demo seed only).
func (s *MongoProductStore) UpsertSKU(ctx context.Context, sku domain.SKU) error {
	_, err := s.skus.ReplaceOne(ctx, bson.D{{Key: "_id", Value: sku.SkuID}}, sku, options.Replace().SetUpsert(true))
	return dependency.Wrap(err)
}
