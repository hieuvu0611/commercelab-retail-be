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
	// CategoryCollection holds the category tree (decision D-01).
	CategoryCollection = "catalog_categories"
	productCollection  = "catalog_products"
)

// MongoCategoryStore implements domain.CategoryStore on MongoDB.
type MongoCategoryStore struct {
	categories *mongo.Collection
	products   *mongo.Collection
}

// NewMongoCategoryStore creates the store and ensures the unique index on path.
func NewMongoCategoryStore(ctx context.Context, store *db.MongoStore) (*MongoCategoryStore, error) {
	d := store.Database()
	s := &MongoCategoryStore{categories: d.Collection(CategoryCollection), products: d.Collection(productCollection)}
	_, err := s.categories.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "path", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_path"),
	})
	if err != nil {
		return nil, fmt.Errorf("create category path index: %w", dependency.Wrap(err))
	}
	return s, nil
}

func (s *MongoCategoryStore) Insert(ctx context.Context, c domain.Category) error {
	if _, err := s.categories.InsertOne(ctx, c); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrCategoryPathTaken
		}
		return dependency.Wrap(err)
	}
	return nil
}

func (s *MongoCategoryStore) Get(ctx context.Context, id string) (domain.Category, error) {
	var c domain.Category
	err := s.categories.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Category{}, domain.ErrCategoryNotFound
	}
	if err != nil {
		return domain.Category{}, dependency.Wrap(err)
	}
	return c, nil
}

// Subtree returns the category at path and its descendants. The path is escaped before it is used in the regex.
func (s *MongoCategoryStore) Subtree(ctx context.Context, path string) ([]domain.Category, error) {
	filter := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "path", Value: path}},
		bson.D{{Key: "path", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(path+"/")}}}},
	}}}
	return s.find(ctx, filter)
}

func (s *MongoCategoryStore) List(ctx context.Context) ([]domain.Category, error) {
	return s.find(ctx, bson.D{})
}

func (s *MongoCategoryStore) find(ctx context.Context, filter bson.D) ([]domain.Category, error) {
	cur, err := s.categories.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "path", Value: 1}}))
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	out := []domain.Category{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, dependency.Wrap(err)
	}
	return out, nil
}

// SaveAll replaces each category by id. Writes are ordered; a path clash stops the batch with
// ErrCategoryPathTaken (the service validates destinations first, so this only guards races).
func (s *MongoCategoryStore) SaveAll(ctx context.Context, cats []domain.Category) error {
	if len(cats) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, len(cats))
	for i, c := range cats {
		models[i] = mongo.NewReplaceOneModel().SetFilter(bson.D{{Key: "_id", Value: c.ID}}).SetReplacement(c)
	}
	if _, err := s.categories.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(true)); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrCategoryPathTaken
		}
		return dependency.Wrap(err)
	}
	return nil
}

func (s *MongoCategoryStore) CountChildren(ctx context.Context, id string) (int64, error) {
	n, err := s.categories.CountDocuments(ctx, bson.D{{Key: "parentId", Value: id}})
	return n, dependency.Wrap(err)
}

// CountProducts counts products that use the category as primary or additional category.
func (s *MongoCategoryStore) CountProducts(ctx context.Context, id string) (int64, error) {
	n, err := s.products.CountDocuments(ctx, bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "primaryCategoryId", Value: id}},
		bson.D{{Key: "categoryIds", Value: id}},
	}}})
	return n, dependency.Wrap(err)
}

func (s *MongoCategoryStore) Delete(ctx context.Context, id string) error {
	_, err := s.categories.DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	return dependency.Wrap(err)
}

var _ domain.CategoryStore = (*MongoCategoryStore)(nil)
