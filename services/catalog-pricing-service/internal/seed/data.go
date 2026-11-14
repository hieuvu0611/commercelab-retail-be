// Package seed loads a realistic demo catalog (categories, products with variants, prices, placeholder images).
// Everything is deterministic (fixed ids, slugs and SKU codes), so loading it again changes nothing.
package seed

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// CategorySpec is a category by path ("backpacks" or "backpacks/city").
type CategorySpec struct {
	Path string
	Name string
}

// ProductSpec describes one seeded product; its SKUs are the combinations of Options (in Axes order).
type ProductSpec struct {
	ID        string
	Slug      string
	Name      string
	Category  string
	Axes      []string
	Options   map[string][]string
	BasePrice int64 // VND, price of the first combination
	Step      int64 // VND added per position of the last axis value
	Material  string
	// SKUCodes overrides generated SKU codes by "value|value" key (the scenario SKUs of SC01).
	SKUCodes map[string]string
}

// SKUSpec is one seeded variant.
type SKUSpec struct {
	Code       string
	Attributes map[string]string
	PriceVND   int64
}

// Categories returns the category tree, parents before children.
func Categories() []CategorySpec {
	return []CategorySpec{
		{"backpacks", "Backpacks"}, {"backpacks/city", "City"}, {"backpacks/travel", "Travel"}, {"backpacks/hiking", "Hiking"},
		{"bottles", "Bottles"}, {"bottles/insulated", "Insulated"}, {"bottles/sports", "Sports"},
		{"travel-accessories", "Travel Accessories"}, {"travel-accessories/packing-cubes", "Packing Cubes"},
		{"travel-accessories/locks", "Locks"}, {"travel-accessories/pouches", "Pouches"},
	}
}

var (
	colorCapacity = []string{"color", "capacity"}
	colorSize     = []string{"color", "size"}
)

// Products returns the demo products (30).
func Products() []ProductSpec {
	bp := func(slug, name, cat string, base, step int64, colors []string, caps ...string) ProductSpec {
		return ProductSpec{Slug: slug, Name: name, Category: cat, Axes: colorCapacity, BasePrice: base, Step: step, Material: "Recycled nylon",
			Options: map[string][]string{"color": colors, "capacity": caps}}
	}
	sized := func(slug, name, cat string, base, step int64, colors []string, sizes ...string) ProductSpec {
		return ProductSpec{Slug: slug, Name: name, Category: cat, Axes: colorSize, BasePrice: base, Step: step, Material: "Ripstop polyester",
			Options: map[string][]string{"color": colors, "size": sizes}}
	}
	plain := func(slug, name string, base int64, colors ...string) ProductSpec {
		return ProductSpec{Slug: slug, Name: name, Category: "travel-accessories/locks", Axes: []string{"color"}, BasePrice: base, Material: "Zinc alloy",
			Options: map[string][]string{"color": colors}}
	}
	city := bp("city-backpack", "City Backpack", "backpacks/city", 300000, 40000, []string{"Black", "Navy", "Grey"}, "20 L", "25 L")
	city.ID = "PROD-BALO-CITY"
	city.SKUCodes = map[string]string{"Black|20 L": "SKU-BALO-BLACK-20L"}
	bottle := bp("insulated-bottle", "Insulated Bottle", "bottles/insulated", 100000, 40000, []string{"Blue", "Black", "White"}, "500 ml", "750 ml")
	bottle.ID = "PROD-BOTTLE-ECO"
	bottle.Material = "Stainless steel 316"
	bottle.SKUCodes = map[string]string{"Blue|500 ml": "SKU-BOTTLE-BLUE-500ML"}

	bottles := func(slug, name, cat string, base, step int64, colors []string, caps ...string) ProductSpec {
		p := bp(slug, name, cat, base, step, colors, caps...)
		p.Material = "Stainless steel"
		return p
	}
	return []ProductSpec{
		city,
		bp("commuter-backpack", "Commuter Backpack", "backpacks/city", 420000, 50000, []string{"Black", "Grey"}, "22 L", "28 L"),
		bp("slim-laptop-backpack", "Slim Laptop Backpack", "backpacks/city", 380000, 40000, []string{"Black", "Navy"}, "18 L", "24 L"),
		bp("urban-daypack", "Urban Daypack", "backpacks/city", 280000, 30000, []string{"Olive", "Black", "Sand"}, "16 L", "20 L"),
		bp("campus-backpack", "Campus Backpack", "backpacks/city", 260000, 30000, []string{"Black", "Red"}, "20 L", "26 L"),
		bp("travel-backpack-40l", "Travel Backpack 40 L", "backpacks/travel", 650000, 90000, []string{"Black", "Olive"}, "40 L", "50 L"),
		bp("carry-on-backpack", "Carry-on Backpack", "backpacks/travel", 720000, 80000, []string{"Black", "Grey"}, "35 L", "40 L"),
		bp("packable-duffel-backpack", "Packable Duffel Backpack", "backpacks/travel", 540000, 70000, []string{"Navy", "Black"}, "45 L", "60 L"),
		bp("trail-hiking-pack", "Trail Hiking Pack", "backpacks/hiking", 890000, 100000, []string{"Orange", "Green"}, "30 L", "40 L"),
		bp("alpine-hiking-pack", "Alpine Hiking Pack", "backpacks/hiking", 1290000, 150000, []string{"Blue", "Black"}, "45 L", "55 L"),
		bp("day-hiker-22l", "Day Hiker 22 L", "backpacks/hiking", 450000, 50000, []string{"Green", "Grey"}, "22 L", "26 L"),
		bottle,
		bottles("insulated-tumbler", "Insulated Tumbler", "bottles/insulated", 220000, 50000, []string{"Black", "White"}, "350 ml", "500 ml"),
		bottles("vacuum-flask", "Vacuum Flask", "bottles/insulated", 350000, 60000, []string{"Steel", "Black"}, "750 ml", "1 L"),
		bottles("kids-insulated-bottle", "Kids Insulated Bottle", "bottles/insulated", 180000, 30000, []string{"Pink", "Blue", "Green"}, "350 ml", "450 ml"),
		bottles("sports-bottle", "Sports Bottle", "bottles/sports", 90000, 20000, []string{"Clear", "Blue", "Green"}, "600 ml", "750 ml"),
		bottles("squeeze-bottle", "Squeeze Bottle", "bottles/sports", 70000, 15000, []string{"Black", "Orange"}, "500 ml", "700 ml"),
		bottles("shaker-bottle", "Shaker Bottle", "bottles/sports", 110000, 20000, []string{"Black", "Purple"}, "600 ml", "800 ml"),
		sized("packing-cube-set", "Packing Cube Set", "travel-accessories/packing-cubes", 250000, 40000, []string{"Black", "Grey"}, "S", "M", "L"),
		sized("compression-packing-cubes", "Compression Packing Cubes", "travel-accessories/packing-cubes", 320000, 40000, []string{"Black", "Navy"}, "M", "L"),
		sized("shoe-bag-cube", "Shoe Bag Cube", "travel-accessories/packing-cubes", 150000, 20000, []string{"Black", "Grey"}, "M", "L"),
		plain("tsa-combination-lock", "TSA Combination Lock", 120000, "Black", "Silver"),
		plain("cable-luggage-lock", "Cable Luggage Lock", 90000, "Black", "Red"),
		plain("padlock-set", "Padlock Set", 140000, "Black", "Silver"),
		plain("luggage-strap-lock", "Luggage Strap Lock", 100000, "Black", "Blue"),
		sized("travel-pouch", "Travel Pouch", "travel-accessories/pouches", 80000, 15000, []string{"Black", "Navy"}, "S", "M"),
		sized("cable-organizer-pouch", "Cable Organizer Pouch", "travel-accessories/pouches", 95000, 15000, []string{"Black", "Grey"}, "S", "M"),
		sized("toiletry-pouch", "Toiletry Pouch", "travel-accessories/pouches", 160000, 20000, []string{"Black", "Pink"}, "M", "L"),
		sized("passport-pouch", "Passport Pouch", "travel-accessories/pouches", 110000, 10000, []string{"Black", "Navy", "Red"}, "S"),
		sized("waterproof-phone-pouch", "Waterproof Phone Pouch", "travel-accessories/pouches", 70000, 10000, []string{"Clear", "Black"}, "M", "L"),
	}
}

// ProductID returns the stable id of the product.
func (p ProductSpec) ProductID() string {
	if p.ID != "" {
		return p.ID
	}
	return "seed-" + p.Slug
}

func compact(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", ".", "", "_", "").Replace(s))
}

// SKUs returns every combination of the options with its code and price.
func (p ProductSpec) SKUs() []SKUSpec {
	combos := []map[string]string{{}}
	var lastAxis string
	for _, axis := range p.Axes {
		lastAxis = axis
		var next []map[string]string
		for _, c := range combos {
			for _, v := range p.Options[axis] {
				n := map[string]string{}
				for k, val := range c {
					n[k] = val
				}
				n[axis] = v
				next = append(next, n)
			}
		}
		combos = next
	}
	out := make([]SKUSpec, 0, len(combos))
	for _, c := range combos {
		values := make([]string, len(p.Axes))
		parts := make([]string, len(p.Axes))
		for i, axis := range p.Axes {
			values[i], parts[i] = c[axis], compact(c[axis])
		}
		code := "SKU-" + strings.ToUpper(p.Slug) + "-" + strings.Join(parts, "-")
		if override, ok := p.SKUCodes[strings.Join(values, "|")]; ok {
			code = override
		}
		idx := 0
		for i, v := range p.Options[lastAxis] {
			if v == c[lastAxis] {
				idx = i
			}
		}
		out = append(out, SKUSpec{Code: code, Attributes: c, PriceVND: p.BasePrice + p.Step*int64(idx)})
	}
	return out
}

// ImageURL is the placeholder picture of a product (an external https URL, served as-is by the storefront).
func (p ProductSpec) ImageURL() string {
	return "https://placehold.co/800x800/png?text=" + url.QueryEscape(p.Name)
}

// PublishedAt is a fixed, increasing time per product so "newest" ordering is stable across runs.
func PublishedAt(index int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Hour)
}

// Description is the demo description of a product.
func (p ProductSpec) Description() string {
	return fmt.Sprintf("%s from the demo catalog. Made with %s.", p.Name, strings.ToLower(p.Material))
}
