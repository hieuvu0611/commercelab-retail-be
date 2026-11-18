import { Injectable, Optional, ServiceUnavailableException } from '@nestjs/common';
import { DatabaseService } from '../../database/database.service';

export interface OlapKpis {
  totalOrders: number;
  confirmedOrders: number;
  deliveredOrders: number;
  grossGmvVnd: number;
  totalDiscountVnd: number;
  codOrders: number;
  prepaidOrders: number;
  avgOrderValueVnd: number;
  openExceptionCases: number;
  totalRmaRequests: number;
}

export interface OlapWarehouseRow {
  warehouseId: string;
  shipments: number;
  delivered: number;
  codToCollectVnd: number;
  availableUnits: number;
  reservedUnits: number;
}

export interface OlapStatusRow {
  status: string;
  orders: number;
  gmvVnd: number;
}

export interface OlapDashboardResponse {
  kpis: OlapKpis;
  byWarehouse: OlapWarehouseRow[];
  byStatus: OlapStatusRow[];
  metadata: {
    source: string;
    database: string;
    period: string;
    lastSyncedAt: string | null;
    lagSeconds: number | null;
    isStaleWarning: boolean;
    policyNotice: string;
  };
}

@Injectable()
export class ClickhouseAnalyticsService {
  private schemaReady = false;

  constructor(@Optional() private readonly db?: DatabaseService) {}

  private config() {
    return {
      url: (process.env.CLICKHOUSE_URL || '').replace(/\/$/, ''),
      db: process.env.CLICKHOUSE_DB || 'commerce_analytics',
      user: process.env.CLICKHOUSE_USER || 'analytics',
      password: process.env.CLICKHOUSE_PASSWORD || 'analytics_secret',
      orderUrl: (process.env.ORDER_SERVICE_URL || 'http://order-checkout-service:8083').replace(/\/$/, ''),
      inventoryUrl: (process.env.INVENTORY_SERVICE_URL || 'http://inventory-warehouse-service:8082').replace(/\/$/, ''),
      fulfillmentUrl: (process.env.FULFILLMENT_SERVICE_URL || 'http://fulfillment-shipping-service:8085').replace(/\/$/, ''),
    };
  }

  private async execClickhouse(sql: string, body?: string): Promise<string> {
    const cfg = this.config();
    if (!cfg.url) {
      throw new ServiceUnavailableException('CLICKHOUSE_URL is not configured');
    }
    const endpoint = `${cfg.url}/?database=${encodeURIComponent(cfg.db)}&query=${encodeURIComponent(sql)}`;
    const res = await fetch(endpoint, {
      method: 'POST',
      headers: {
        'X-ClickHouse-User': cfg.user,
        'X-ClickHouse-Key': cfg.password,
        ...(body ? { 'Content-Type': 'application/x-ndjson' } : {}),
      },
      body: body ?? '',
    });
    if (!res.ok) {
      const txt = await res.text();
      throw new Error(`ClickHouse error ${res.status}: ${txt}`);
    }
    return res.text();
  }

  async ensureSchema(): Promise<void> {
    if (this.schemaReady) return;
    const cfg = this.config();
    if (!cfg.url) return;

    const createDbEndpoint = `${cfg.url}/?query=${encodeURIComponent(`CREATE DATABASE IF NOT EXISTS ${cfg.db}`)}`;
    const dbRes = await fetch(createDbEndpoint, {
      method: 'POST',
      headers: {
        'X-ClickHouse-User': cfg.user,
        'X-ClickHouse-Key': cfg.password,
      },
    });
    if (!dbRes.ok) {
      throw new Error(`ClickHouse CREATE DATABASE failed: ${dbRes.status}`);
    }

    await this.execClickhouse(`
      CREATE TABLE IF NOT EXISTS order_facts (
        order_id String,
        status String,
        payment_method String,
        warehouse_id String,
        total_payable_vnd Int64,
        discount_vnd Int64,
        created_at DateTime64(3, 'UTC'),
        synced_at DateTime64(3, 'UTC')
      ) ENGINE = MergeTree() ORDER BY (order_id)
    `);
    await this.execClickhouse(`
      CREATE TABLE IF NOT EXISTS shipment_facts (
        shipment_id String,
        order_id String,
        warehouse_id String,
        status String,
        cod_amount_vnd Int64,
        synced_at DateTime64(3, 'UTC')
      ) ENGINE = MergeTree() ORDER BY (shipment_id)
    `);
    await this.execClickhouse(`
      CREATE TABLE IF NOT EXISTS warehouse_inventory_facts (
        sku_id String,
        warehouse_id String,
        available_qty Int64,
        reserved_qty Int64,
        synced_at DateTime64(3, 'UTC')
      ) ENGINE = MergeTree() ORDER BY (warehouse_id, sku_id)
    `);
    await this.execClickhouse(`
      CREATE TABLE IF NOT EXISTS sync_watermarks (
        pipeline String,
        synced_at DateTime64(3, 'UTC'),
        orders_count Int64,
        shipments_count Int64,
        inventory_rows Int64
      ) ENGINE = MergeTree() ORDER BY (synced_at)
    `);
    this.schemaReady = true;
  }

  private async fetchServiceList(
    url: string,
    authHeader?: string,
    paginated = false,
  ): Promise<any[]> {
    const internalToken = process.env.INTERNAL_SERVICE_TOKEN;
    const headers = {
      ...(internalToken ? { 'X-Internal-Token': internalToken } : {}),
      ...(authHeader ? { Authorization: authHeader } : {}),
    };

    if (!paginated) {
      const res = await fetch(url, { headers });
      if (!res.ok) {
        throw new Error(`Upstream analytics fetch ${url} failed with ${res.status}`);
      }
      const json = (await res.json()) as { data?: any[] };
      return Array.isArray(json?.data) ? json.data : [];
    }

    const out: any[] = [];
    let page = 1;
    let pageCount = 1;
    do {
      const sep = url.includes('?') ? '&' : '?';
      const pagedUrl = `${url}${sep}limit=100&page=${page}`;
      const res = await fetch(pagedUrl, { headers });
      if (!res.ok) {
        throw new Error(`Upstream analytics fetch ${pagedUrl} failed with ${res.status}`);
      }
      const json = (await res.json()) as {
        data?: any[];
        metadata?: { pageCount?: number };
      };
      const rows = Array.isArray(json?.data) ? json.data : [];
      out.push(...rows);
      pageCount = Math.max(1, Number(json?.metadata?.pageCount || 1));
      page++;
    } while (page <= pageCount && page <= 1000);

    return out;
  }

  private toClickhouseDateTime(iso?: string): string {
    const d = iso ? new Date(iso) : new Date();
    const safe = Number.isNaN(d.getTime()) ? new Date() : d;
    return safe.toISOString().replace('T', ' ').replace('Z', '');
  }

  async syncFromSources(authHeader?: string) {
    await this.ensureSchema();
    const cfg = this.config();
    const nowIso = this.toClickhouseDateTime();

    const [orders, shipments, stockRows] = await Promise.all([
      this.fetchServiceList(`${cfg.orderUrl}/api/v1/orders/internal`, authHeader, true),
      this.fetchServiceList(`${cfg.fulfillmentUrl}/api/v1/shipments/internal`, authHeader, true),
      this.fetchServiceList(`${cfg.inventoryUrl}/api/v1/inventory/internal/stock`, authHeader, false),
    ]);

    await this.execClickhouse('TRUNCATE TABLE IF EXISTS order_facts');
    await this.execClickhouse('TRUNCATE TABLE IF EXISTS shipment_facts');
    await this.execClickhouse('TRUNCATE TABLE IF EXISTS warehouse_inventory_facts');

    if (orders.length > 0) {
      const ndjson = orders
        .map((o) =>
          JSON.stringify({
            order_id: String(o.orderId || ''),
            status: String(o.status || ''),
            payment_method: String(o.paymentMethod || 'PREPAID'),
            warehouse_id: String(o.warehouseId || 'WH-HCM-01'),
            total_payable_vnd: Number(o.totalPayableVnd || 0),
            discount_vnd: Number(o.discountVnd || 0),
            created_at: this.toClickhouseDateTime(o.createdAt),
            synced_at: nowIso,
          }),
        )
        .join('\n');
      await this.execClickhouse('INSERT INTO order_facts FORMAT JSONEachRow', ndjson);
    }

    if (shipments.length > 0) {
      const ndjson = shipments
        .map((s) =>
          JSON.stringify({
            shipment_id: String(s.shipmentId || ''),
            order_id: String(s.orderId || ''),
            warehouse_id: String(s.warehouseId || 'WH-HCM-01'),
            status: String(s.status || ''),
            cod_amount_vnd: Number(s.codAmountToCollect || 0),
            synced_at: nowIso,
          }),
        )
        .join('\n');
      await this.execClickhouse('INSERT INTO shipment_facts FORMAT JSONEachRow', ndjson);
    }

    if (stockRows.length > 0) {
      const ndjson = stockRows
        .map((st) =>
          JSON.stringify({
            sku_id: String(st.skuId || ''),
            warehouse_id: String(st.warehouseId || 'WH-HCM-01'),
            available_qty: Number(st.availableQty ?? st.available ?? 0),
            reserved_qty: Number(st.reservedQty ?? st.reserved ?? 0),
            synced_at: nowIso,
          }),
        )
        .join('\n');
      await this.execClickhouse('INSERT INTO warehouse_inventory_facts FORMAT JSONEachRow', ndjson);
    }

    await this.execClickhouse(
      'INSERT INTO sync_watermarks FORMAT JSONEachRow',
      JSON.stringify({
        pipeline: 'backoffice-manual-sync',
        synced_at: nowIso,
        orders_count: orders.length,
        shipments_count: shipments.length,
        inventory_rows: stockRows.length,
      }),
    );

    return {
      syncedAt: new Date().toISOString(),
      ordersSynced: orders.length,
      shipmentsSynced: shipments.length,
      inventoryRowsSynced: stockRows.length,
      source: 'ClickHouse OLAP',
    };
  }

  private async queryJsonRows<T = Record<string, any>>(sql: string): Promise<T[]> {
    const raw = await this.execClickhouse(`${sql} FORMAT JSON`);
    const parsed = JSON.parse(raw) as { data?: T[] };
    return Array.isArray(parsed?.data) ? parsed.data : [];
  }

  async getOlapDashboard(period: string = '30D'): Promise<OlapDashboardResponse> {
    await this.ensureSchema();
    const cfg = this.config();

    const [orderAggRows, statusRows, shipWhRows, invWhRows, watermarkRows] = await Promise.all([
      this.queryJsonRows(`
        SELECT
          count() AS totalOrders,
          countIf(status IN ('CONFIRMED', 'FULFILLING', 'PARTIALLY_DELIVERED', 'DELIVERED')) AS confirmedOrders,
          countIf(status = 'DELIVERED') AS deliveredOrders,
          sum(total_payable_vnd) AS grossGmvVnd,
          sum(discount_vnd) AS totalDiscountVnd,
          countIf(payment_method = 'COD') AS codOrders,
          countIf(payment_method != 'COD') AS prepaidOrders
        FROM order_facts
      `),
      this.queryJsonRows(`
        SELECT status, count() AS orders, sum(total_payable_vnd) AS gmvVnd
        FROM order_facts
        GROUP BY status
        ORDER BY orders DESC
      `),
      this.queryJsonRows(`
        SELECT
          warehouse_id AS warehouseId,
          count() AS shipments,
          countIf(status = 'DELIVERED') AS delivered,
          sum(cod_amount_vnd) AS codToCollectVnd
        FROM shipment_facts
        GROUP BY warehouse_id
      `),
      this.queryJsonRows(`
        SELECT
          warehouse_id AS warehouseId,
          sum(available_qty) AS availableUnits,
          sum(reserved_qty) AS reservedUnits
        FROM warehouse_inventory_facts
        GROUP BY warehouse_id
      `),
      this.queryJsonRows(`
        SELECT toString(max(synced_at)) AS lastSyncedAt
        FROM sync_watermarks
      `),
    ]);

    let openCases = 0;
    let totalRma = 0;
    if (this.db) {
      const [cRes, rRes] = await Promise.all([
        this.db.query(`SELECT COUNT(*) AS cnt FROM support.exception_cases WHERE status IN ('OPEN_UNASSIGNED', 'ASSIGNED_IN_PROGRESS')`),
        this.db.query(`SELECT COUNT(*) AS cnt FROM support.rma_requests`),
      ]);
      openCases = Number(cRes.rows?.[0]?.cnt || 0);
      totalRma = Number(rRes.rows?.[0]?.cnt || 0);
    }

    const agg = orderAggRows[0] || {};
    const totalOrders = Number(agg.totalOrders || 0);
    const grossGmvVnd = Number(agg.grossGmvVnd || 0);

    const whMap = new Map<string, OlapWarehouseRow>();
    const ensureWh = (wh: string): OlapWarehouseRow => {
      const existing = whMap.get(wh);
      if (existing) return existing;
      const fresh: OlapWarehouseRow = {
        warehouseId: wh,
        shipments: 0,
        delivered: 0,
        codToCollectVnd: 0,
        availableUnits: 0,
        reservedUnits: 0,
      };
      whMap.set(wh, fresh);
      return fresh;
    };
    for (const s of shipWhRows) {
      const row = ensureWh(String(s.warehouseId || 'WH-HCM-01'));
      row.shipments = Number(s.shipments || 0);
      row.delivered = Number(s.delivered || 0);
      row.codToCollectVnd = Number(s.codToCollectVnd || 0);
    }
    for (const inv of invWhRows) {
      const row = ensureWh(String(inv.warehouseId || 'WH-HCM-01'));
      row.availableUnits = Number(inv.availableUnits || 0);
      row.reservedUnits = Number(inv.reservedUnits || 0);
    }

    const rawSync = watermarkRows[0]?.lastSyncedAt ? String(watermarkRows[0].lastSyncedAt) : '';
    const lastSyncedIso =
      rawSync && !rawSync.startsWith('1970-01-01')
        ? new Date(rawSync.replace(' ', 'T') + (rawSync.endsWith('Z') ? '' : 'Z')).toISOString()
        : null;
    const lagSeconds = lastSyncedIso
      ? Math.max(0, Math.round((Date.now() - Date.parse(lastSyncedIso)) / 1000))
      : null;

    return {
      kpis: {
        totalOrders,
        confirmedOrders: Number(agg.confirmedOrders || 0),
        deliveredOrders: Number(agg.deliveredOrders || 0),
        grossGmvVnd,
        totalDiscountVnd: Number(agg.totalDiscountVnd || 0),
        codOrders: Number(agg.codOrders || 0),
        prepaidOrders: Number(agg.prepaidOrders || 0),
        avgOrderValueVnd: totalOrders > 0 ? Math.round(grossGmvVnd / totalOrders) : 0,
        openExceptionCases: openCases,
        totalRmaRequests: totalRma,
      },
      byWarehouse: Array.from(whMap.values()).sort((a, b) => a.warehouseId.localeCompare(b.warehouseId)),
      byStatus: statusRows.map((r) => ({
        status: String(r.status || ''),
        orders: Number(r.orders || 0),
        gmvVnd: Number(r.gmvVnd || 0),
      })),
      metadata: {
        source: 'ClickHouse OLAP',
        database: cfg.db,
        period,
        lastSyncedAt: lastSyncedIso,
        lagSeconds,
        isStaleWarning: lagSeconds === null || lagSeconds > 300,
        policyNotice:
          'SC12: ClickHouse OLAP analytics are eventually consistent read-models and never drive transactional stock, checkout, or refund decisions.',
      },
    };
  }
}
