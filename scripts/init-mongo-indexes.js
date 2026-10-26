// =============================================================================
// Commerce Lab Retail — Khởi tạo Collections, TTL Indexes & Seed Catalog trên MongoDB
// Database: commerce_lab_doc
// =============================================================================

const dbRef = db.getSiblingDB('commerce_lab_doc');

// 1. Catalog Products (B01 — Thông số kỹ thuật đa ngành hàng)
dbRef.catalog_products.createIndex({ product_id: 1 }, { unique: true });
dbRef.catalog_products.createIndex({ category_type: 1, status: 1 });

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

// Seed 2 sản phẩm chuẩn SC01 (Balo City & Bình nước) với cấu trúc thông số khác nhau
dbRef.catalog_products.updateOne(
  { product_id: 'PROD-BALO-CITY' },
  {
    $setOnInsert: {
      product_id: 'PROD-BALO-CITY',
      name: 'Balo City Chống Nước Đa Năng',
      category_type: 'BACKPACK',
      status: 'PUBLISHED',
      specs: {
        capacity_liters: 20,
        laptop_compartment_inch: 15.6,
        material: 'Cordura Ballistic Nylon',
        water_resistance: 'IPX4',
      },
      skus: [
        {
          sku_id: 'SKU-BALO-BLACK-20L',
          color: 'Đen',
          size: '20L',
          unit_price_vnd: 300000,
        },
      ],
      created_at: new Date(),
    },
  },
  { upsert: true }
);

dbRef.catalog_products.updateOne(
  { product_id: 'PROD-BOTTLE-ECO' },
  {
    $setOnInsert: {
      product_id: 'PROD-BOTTLE-ECO',
      name: 'Bình Nước Giữ Nhiệt Eco',
      category_type: 'THERMOS_BOTTLE',
      status: 'PUBLISHED',
      specs: {
        volume_ml: 500,
        keep_hot_hours: 12,
        keep_cold_hours: 24,
        inner_core_material: 'Inox 316',
      },
      skus: [
        {
          sku_id: 'SKU-BOTTLE-BLUE-500ML',
          color: 'Xanh',
          size: '500ml',
          unit_price_vnd: 100000,
        },
      ],
      created_at: new Date(),
    },
  },
  { upsert: true }
);
