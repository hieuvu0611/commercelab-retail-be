// =============================================================================
// Commerce Lab Retail — Khởi tạo Collections, TTL Indexes & Seed Catalog trên MongoDB
// Database: commerce_lab_doc
// =============================================================================

const dbRef = db.getSiblingDB('commerce_lab_doc');

// 1. Catalog Products (B01 — Thông số kỹ thuật đa ngành hàng)
// Index names match the ones catalog-pricing-service creates at start-up; a different name makes it fail with
// IndexOptionsConflict on a fresh database.
dbRef.catalog_products.createIndex({ slug: 1 }, { unique: true, name: 'uniq_slug' });
dbRef.catalog_products.createIndex({ status: 1, primaryCategoryId: 1 });
dbRef.catalog_products.createIndex({ categoryIds: 1 });
dbRef.catalog_sku_specs.createIndex({ productId: 1 }, { name: 'by_product' });

// 2. Order Commercial Snapshots (I04 — Hợp đồng đóng băng lúc mua)
dbRef.order_commercial_snapshots.createIndex({ order_id: 1 }, { unique: true });

// 3. Fulfillment Shipment Timelines (B04 — 30-50 mốc vận đơn dùng $push)
dbRef.fulfillment_timelines.createIndex({ shipment_id: 1 }, { unique: true });
dbRef.fulfillment_timelines.createIndex({ order_id: 1 });

// 4. Support Exception Cases & RMA Dossiers (Mục 17, B06 — Hồ sơ sống & SLA 30p)
dbRef.support_exception_cases.createIndex({ case_id: 1 }, { unique: true });
dbRef.support_exception_cases.createIndex({ status: 1, owner_id: 1, sla_due_at: 1 });

// 5. Payment Raw Webhooks (B03, SC05 — Tự động xóa sau 90 ngày bằng TTL Index)
dbRef.payment_raw_webhooks.createIndex({ received_at: 1 }, { expireAfterSeconds: 7776000 });

// 6. Notification User Inboxes (Notification Service — Tự động xóa sau 90 ngày bằng TTL Index)
dbRef.notify_user_inboxes.createIndex({ recipient_id: 1, is_read: 1, created_at: -1 });
dbRef.notify_user_inboxes.createIndex({ created_at: 1 }, { expireAfterSeconds: 7776000 });

// Seed 2 sản phẩm chuẩn SC01 (Balo City & Bình nước). Product model of P2-T03: camelCase fields, variant axes,
// SKUs in catalog_sku_specs (their prices live in Postgres pricing.sku_prices).
dbRef.catalog_products.updateOne(
  { _id: 'PROD-BALO-CITY' },
  {
    $setOnInsert: {
      _id: 'PROD-BALO-CITY',
      slug: 'city-backpack',
      name: 'Balo City Chống Nước Đa Năng',
      description: '',
      brand: '',
      primaryCategoryId: '',
      categoryIds: [],
      variantAxes: ['color', 'capacity'],
      status: 'PUBLISHED',
      specs: {
        capacityLiters: 20,
        laptopCompartmentInch: 15.6,
        material: 'Cordura Ballistic Nylon',
        waterResistance: 'IPX4',
      },
      images: [],
      version: 1,
      createdBy: 'seed',
      updatedBy: 'seed',
      createdAt: new Date(),
      updatedAt: new Date(),
    },
  },
  { upsert: true }
);
dbRef.catalog_sku_specs.updateOne(
  { _id: 'SKU-BALO-BLACK-20L' },
  {
    $setOnInsert: {
      _id: 'SKU-BALO-BLACK-20L',
      productId: 'PROD-BALO-CITY',
      attributes: { color: 'Đen', capacity: '20L' },
      isActive: true,
      createdAt: new Date(),
      updatedAt: new Date(),
    },
  },
  { upsert: true }
);

dbRef.catalog_products.updateOne(
  { _id: 'PROD-BOTTLE-ECO' },
  {
    $setOnInsert: {
      _id: 'PROD-BOTTLE-ECO',
      slug: 'eco-insulated-bottle',
      name: 'Bình Nước Giữ Nhiệt Eco',
      description: '',
      brand: '',
      primaryCategoryId: '',
      categoryIds: [],
      variantAxes: ['color', 'capacity'],
      status: 'PUBLISHED',
      specs: {
        volumeMl: 500,
        keepHotHours: 12,
        keepColdHours: 24,
        innerCoreMaterial: 'Inox 316',
      },
      images: [],
      version: 1,
      createdBy: 'seed',
      updatedBy: 'seed',
      createdAt: new Date(),
      updatedAt: new Date(),
    },
  },
  { upsert: true }
);
dbRef.catalog_sku_specs.updateOne(
  { _id: 'SKU-BOTTLE-BLUE-500ML' },
  {
    $setOnInsert: {
      _id: 'SKU-BOTTLE-BLUE-500ML',
      productId: 'PROD-BOTTLE-ECO',
      attributes: { color: 'Xanh', capacity: '500ml' },
      isActive: true,
      createdAt: new Date(),
      updatedAt: new Date(),
    },
  },
  { upsert: true }
);
