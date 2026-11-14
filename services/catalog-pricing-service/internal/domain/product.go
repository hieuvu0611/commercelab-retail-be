package domain

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ProductStatus is the publishing state of a product.
type ProductStatus string

const (
	ProductDraft       ProductStatus = "DRAFT"
	ProductInReview    ProductStatus = "IN_REVIEW"
	ProductPublished   ProductStatus = "PUBLISHED"
	ProductUnpublished ProductStatus = "UNPUBLISHED"
)

func (s ProductStatus) valid() bool {
	switch s {
	case ProductDraft, ProductInReview, ProductPublished, ProductUnpublished:
		return true
	}
	return false
}

const (
	maxProductNameLen = 200
	maxVariantAxes    = 5
	maxAttributeLen   = 64
	maxSpecKeyNesting = 8
)

var (
	axisPattern    = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,31}$`)
	skuCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{1,63}$`)
)

// ProductImage is a picture of a product; its position in Product.Images is its display order.
type ProductImage struct {
	Key string `json:"key" bson:"key"`
	Alt string `json:"alt" bson:"alt"`
}

// Product is a catalog product. Variants (SKUs) are stored separately and described by VariantAxes, for example
// ["color", "capacity"]. Specs hold free-form technical attributes with camelCase keys.
type Product struct {
	ID                string         `json:"id" bson:"_id"`
	Slug              string         `json:"slug" bson:"slug"`
	Name              string         `json:"name" bson:"name"`
	Description       string         `json:"description" bson:"description"`
	Brand             string         `json:"brand" bson:"brand"`
	PrimaryCategoryID string         `json:"primaryCategoryId" bson:"primaryCategoryId"`
	CategoryIDs       []string       `json:"categoryIds" bson:"categoryIds"`
	VariantAxes       []string       `json:"variantAxes" bson:"variantAxes"`
	Specs             map[string]any `json:"specs" bson:"specs"`
	Images            []ProductImage `json:"images" bson:"images"`
	Status            ProductStatus  `json:"status" bson:"status"`
	Version           int64          `json:"version" bson:"version"`
	CreatedBy         string         `json:"createdBy" bson:"createdBy"`
	UpdatedBy         string         `json:"updatedBy" bson:"updatedBy"`
	CreatedAt         time.Time      `json:"createdAt" bson:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt" bson:"updatedAt"`
}

// SKU is one orderable variant of a product. Its code is globally unique; Attributes hold one value per
// variant axis of the product.
type SKU struct {
	SkuID      string            `json:"skuId" bson:"_id"`
	ProductID  string            `json:"productId" bson:"productId"`
	Attributes map[string]string `json:"attributes" bson:"attributes"`
	IsActive   bool              `json:"isActive" bson:"isActive"`
	CreatedAt  time.Time         `json:"createdAt" bson:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt" bson:"updatedAt"`
}

// ValidateProduct checks the structural rules every stored product must satisfy. Completeness required for
// publishing (images, description, SKUs, prices) is checked by the publish workflow, not here.
func ValidateProduct(p Product) error {
	fields := map[string][]string{}
	add := func(field, msg string) { fields[field] = append(fields[field], msg) }

	if name := strings.TrimSpace(p.Name); name == "" {
		add("name", "name is required")
	} else if len(name) > maxProductNameLen {
		add("name", fmt.Sprintf("name must be at most %d characters", maxProductNameLen))
	}
	if len(p.Slug) == 0 || len(p.Slug) > maxCategorySlugLen || !categorySlugPattern.MatchString(p.Slug) {
		add("slug", "slug must be 1-64 characters of lowercase letters, digits and single hyphens")
	}
	if !p.Status.valid() {
		add("status", "status must be DRAFT, IN_REVIEW, PUBLISHED or UNPUBLISHED")
	}
	if len(p.VariantAxes) > maxVariantAxes {
		add("variantAxes", fmt.Sprintf("at most %d variant axes are allowed", maxVariantAxes))
	}
	seenAxis := map[string]bool{}
	for _, axis := range p.VariantAxes {
		if !axisPattern.MatchString(axis) {
			add("variantAxes", fmt.Sprintf("axis %q must be a camelCase word of letters and digits", axis))
		}
		if seenAxis[axis] {
			add("variantAxes", fmt.Sprintf("axis %q is listed twice", axis))
		}
		seenAxis[axis] = true
	}
	seenCategory := map[string]bool{}
	for _, id := range p.CategoryIDs {
		if seenCategory[id] {
			add("categoryIds", fmt.Sprintf("category %q is listed twice", id))
		}
		seenCategory[id] = true
	}
	if p.PrimaryCategoryID != "" && !seenCategory[p.PrimaryCategoryID] {
		add("categoryIds", "categoryIds must include the primary category")
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// NormalizeProduct returns a copy of p whose spec keys are camelCase and whose categoryIds include the
// primary category. The input is not modified.
func NormalizeProduct(p Product) (Product, error) {
	specs, err := NormalizeSpecs(p.Specs)
	if err != nil {
		return Product{}, &ValidationError{Fields: map[string][]string{"specs": {err.Error()}}}
	}
	p.Specs = specs
	p.CategoryIDs = slices.Clone(p.CategoryIDs)
	if p.PrimaryCategoryID != "" && !slices.Contains(p.CategoryIDs, p.PrimaryCategoryID) {
		p.CategoryIDs = append([]string{p.PrimaryCategoryID}, p.CategoryIDs...)
	}
	return p, nil
}

// NormalizeSpecKey converts snake_case, kebab-case and spaced keys to camelCase; camelCase keys are kept
// (only their first letter is lowered).
func NormalizeSpecKey(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		runes := []rune(parts[0])
		runes[0] = unicode.ToLower(runes[0])
		return string(runes)
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, part := range parts[1:] {
		runes := []rune(strings.ToLower(part))
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}

// NormalizeSpecs returns a deep copy of specs with every map key (at any depth) converted by NormalizeSpecKey.
// Two keys that normalize to the same name, or a key without letters or digits, are an error. A nil input
// yields an empty map.
func NormalizeSpecs(specs map[string]any) (map[string]any, error) {
	out, err := normalizeMap(reflect.ValueOf(specs), 0)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeMap(v reflect.Value, depth int) (map[string]any, error) {
	if depth > maxSpecKeyNesting {
		return nil, fmt.Errorf("specs are nested deeper than %d levels", maxSpecKeyNesting)
	}
	out := map[string]any{}
	if !v.IsValid() || v.IsNil() {
		return out, nil
	}
	iter := v.MapRange()
	for iter.Next() {
		key := NormalizeSpecKey(iter.Key().String())
		if key == "" {
			return nil, fmt.Errorf("spec key %q is empty after normalization", iter.Key().String())
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("spec key %q appears more than once after normalization", key)
		}
		val, err := normalizeValue(iter.Value(), depth+1)
		if err != nil {
			return nil, err
		}
		out[key] = val
	}
	return out, nil
}

func normalizeValue(v reflect.Value, depth int) (any, error) {
	for v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil, nil
	}
	switch {
	case v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String:
		return normalizeMap(v, depth)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8:
		out := make([]any, v.Len())
		for i := range out {
			item, err := normalizeValue(v.Index(i), depth)
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	}
	return v.Interface(), nil
}

func trimmedAttributes(attrs map[string]string) map[string]string {
	out := make(map[string]string, len(attrs))
	for k, v := range attrs {
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// combinationKey identifies a variant by its axis values, ignoring case and surrounding spaces.
func combinationKey(axes []string, attrs map[string]string) string {
	values := make([]string, len(axes))
	for i, axis := range axes {
		values[i] = strings.ToLower(strings.TrimSpace(attrs[axis]))
	}
	return strings.Join(values, "\x1f")
}

// ValidateSKU checks s against its product: a well-formed code, one non-blank value for every variant axis and
// no other attribute, and a combination not used by another SKU of siblings (a sibling with the same code is
// the SKU itself and is ignored). Global uniqueness of the code is enforced by the store.
func ValidateSKU(p Product, s SKU, siblings []SKU) error {
	fields := map[string][]string{}
	add := func(field, msg string) { fields[field] = append(fields[field], msg) }

	if !skuCodePattern.MatchString(s.SkuID) {
		add("skuId", "SKU code must be 2-64 characters of uppercase letters, digits, dot, underscore and hyphen")
	}
	for _, axis := range p.VariantAxes {
		value := strings.TrimSpace(s.Attributes[axis])
		if value == "" {
			add("attributes."+axis, fmt.Sprintf("a value for %q is required", axis))
		} else if len(value) > maxAttributeLen {
			add("attributes."+axis, fmt.Sprintf("value must be at most %d characters", maxAttributeLen))
		}
	}
	for key := range s.Attributes {
		if !slices.Contains(p.VariantAxes, key) {
			add("attributes."+key, fmt.Sprintf("%q is not a variant axis of this product", key))
		}
	}
	if len(fields) == 0 {
		mine := combinationKey(p.VariantAxes, s.Attributes)
		for _, other := range siblings {
			if other.SkuID != s.SkuID && combinationKey(p.VariantAxes, other.Attributes) == mine {
				add("attributes", fmt.Sprintf("another SKU (%s) already has this combination", other.SkuID))
				break
			}
		}
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// DuplicateSKUCodes returns the codes that occur more than once in skus, sorted.
func DuplicateSKUCodes(skus []SKU) []string {
	seen := map[string]int{}
	for _, s := range skus {
		seen[s.SkuID]++
	}
	var dups []string
	for code, n := range seen {
		if n > 1 {
			dups = append(dups, code)
		}
	}
	slices.Sort(dups)
	return dups
}

// ErrSKUCodeTaken is returned by a store when another SKU already uses the code.
var ErrSKUCodeTaken = errors.New("SKU code is already used by another SKU")
