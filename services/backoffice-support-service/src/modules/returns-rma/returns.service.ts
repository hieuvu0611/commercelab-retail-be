import {
  BadRequestException,
  ConflictException,
  ForbiddenException,
  Injectable,
  NotFoundException,
  OnModuleInit,
  ServiceUnavailableException,
} from '@nestjs/common';
import { CreateReturnRequestDto } from './create-return.dto';
import { DatabaseService } from '../../database/database.service';
import { PaymentClient } from './payment.client';
import { MakerCheckerPolicyService } from '../../common/guards/maker-checker.guard';
import { resolveInternalServiceToken } from '../../common/guards/jwt-auth.guard';

export interface OrderLineCeiling {
  orderId: string;
  skuId: string;
  customerId?: string;
  warehouseId?: string;
  deliveredQty: number;
  returnedAcceptedQty: number;
  returnOpenQty: number;
  allocatedUnitPaidVnd: number;
  shippingFeeVnd: number;
  deliveredAt: string;
}

export interface ReturnCaseLine {
  skuId: string;
  skuName: string;
  quantity: number;
  receivedQuantity: number;
  qcPassedQuantity: number;
  qcFailedQuantity: number;
  allocatedUnitPaidVnd: number;
}

export interface ReturnCaseRecord {
  returnId: string;
  orderId: string;
  customerId: string;
  warehouseId: string;
  status:
    | 'REQUESTED'
    | 'APPROVED'
    | 'RECEIVED'
    | 'QC_PASSED'
    | 'QC_FAILED'
    | 'REFUND_PENDING'
    | 'REFUNDED'
    | 'REJECTED';
  reason: string;
  items: ReturnCaseLine[];
  refundableAmountVnd: number;
  shippingRefundVnd: number;
  refund: {
    amountVnd: number;
    goodsVnd: number;
    shippingVnd: number;
    status: 'PENDING' | 'COMPLETED';
  } | null;
  isExchange: boolean;
  linkedOrderId?: string;
  customerNote?: string;
  rejectionNote: string;
  createdAt: string;
  updatedAt: string;
  timeline: Array<{
    at: string;
    actor: string;
    note: string;
  }>;
}

const SEVEN_DAYS_MS = 7 * 24 * 60 * 60 * 1000;

@Injectable()
export class ReturnsRmaService implements OnModuleInit {
  private readonly ceilings = new Map<string, OrderLineCeiling>();
  private readonly returnCases = new Map<string, ReturnCaseRecord>();
  private hydrated = false;

  constructor(
    private readonly db: DatabaseService,
    private readonly paymentClient: PaymentClient,
    private readonly makerChecker: MakerCheckerPolicyService,
  ) {
    // Seed standard SC01 / SC07 order ceilings for O-001 (delivered now)
    const nowIso = new Date().toISOString();
    this.seedCeiling({
      orderId: 'O-001',
      skuId: 'SKU-BALO-BLACK-20L',
      deliveredQty: 2,
      returnedAcceptedQty: 0,
      returnOpenQty: 0,
      allocatedUnitPaidVnd: 270_000,
      shippingFeeVnd: 30_000,
      deliveredAt: nowIso,
    });
    this.seedCeiling({
      orderId: 'O-001',
      skuId: 'SKU-BOTTLE-BLUE-500ML',
      deliveredQty: 1,
      returnedAcceptedQty: 0,
      returnOpenQty: 0,
      allocatedUnitPaidVnd: 90_000,
      shippingFeeVnd: 30_000,
      deliveredAt: nowIso,
    });
  }

  async onModuleInit(): Promise<void> {
    await this.ensureHydrated();
  }

  private dbReady(): boolean {
    if (!this.db) return false;
    // DatabaseService always initializes a Pool on module init; only query it in Nest runtime when DATABASE_URL_SUPPORT is configured.
    if (typeof (this.db as any).getPool === 'function') {
      return Boolean(process.env.DATABASE_URL_SUPPORT && (this.db as any).getPool());
    }
    return true;
  }

  async ensureHydrated(): Promise<void> {
    if (this.hydrated || !this.dbReady()) return;
    this.hydrated = true;

    const reqRes = await this.db.query(
      `SELECT rma_id, order_id, customer_id, warehouse_id, reason, status,
              refundable_amount_vnd, shipping_refund_vnd, is_exchange, linked_order_id, notes,
              customer_note, created_at, updated_at
       FROM support.rma_requests
       ORDER BY created_at DESC`,
    );
    if (!reqRes.rows || reqRes.rows.length === 0) return;

    const rmaIds = reqRes.rows.map((r: any) => String(r.rma_id));
    const itemRes = await this.db.query(
      `SELECT rma_id, sku_id, sku_name, quantity, unit_paid_vnd, qc_status,
              received_qty, qc_passed_qty, qc_failed_qty
       FROM support.rma_items
       WHERE rma_id = ANY($1)`,
      [rmaIds],
    );
    const tlRes = await this.db.query(
      `SELECT rma_id, status, actor, note, created_at
       FROM support.rma_timeline
       WHERE rma_id = ANY($1)
       ORDER BY created_at ASC, id ASC`,
      [rmaIds],
    );

    const itemsByRma = new Map<string, ReturnCaseLine[]>();
    for (const row of itemRes.rows || []) {
      const rmaId = String(row.rma_id);
      const list = itemsByRma.get(rmaId) || [];
      const qty = Number(row.quantity);
      const qc = String(row.qc_status || 'PENDING');
      const receivedQty = Number(row.received_qty ?? (qc !== 'PENDING' ? qty : 0));
      const qcPassedQty = Number(row.qc_passed_qty ?? (qc === 'PASS' ? qty : 0));
      const qcFailedQty = Number(row.qc_failed_qty ?? (qc === 'FAIL' ? qty : 0));
      list.push({
        skuId: String(row.sku_id),
        skuName: String(row.sku_name || row.sku_id),
        quantity: qty,
        receivedQuantity: receivedQty,
        qcPassedQuantity: qcPassedQty,
        qcFailedQuantity: qcFailedQty,
        allocatedUnitPaidVnd: Number(row.unit_paid_vnd),
      });
      itemsByRma.set(rmaId, list);
    }

    const tlByRma = new Map<string, Array<{ at: string; actor: string; note: string }>>();
    for (const row of tlRes.rows || []) {
      const rmaId = String(row.rma_id);
      const list = tlByRma.get(rmaId) || [];
      list.push({
        at: new Date(row.created_at).toISOString(),
        actor: String(row.actor),
        note: String(row.note || ''),
      });
      tlByRma.set(rmaId, list);
    }

    for (const row of reqRes.rows) {
      const rmaId = String(row.rma_id);
      if (this.returnCases.has(rmaId)) continue;
      const items = itemsByRma.get(rmaId) || [];
      const refundTotal = Number(row.refundable_amount_vnd);
      const shippingRefund = Number(row.shipping_refund_vnd);
      const status = String(row.status) as ReturnCaseRecord['status'];
      this.returnCases.set(rmaId, {
        returnId: rmaId,
        orderId: String(row.order_id),
        customerId: String(row.customer_id),
        warehouseId: String(row.warehouse_id || 'WH-HCM-01'),
        status,
        reason: String(row.reason),
        items,
        refundableAmountVnd: refundTotal,
        shippingRefundVnd: shippingRefund,
        refund: {
          amountVnd: refundTotal,
          goodsVnd: Math.max(0, refundTotal - shippingRefund),
          shippingVnd: shippingRefund,
          status: status === 'REFUNDED' ? 'COMPLETED' : 'PENDING',
        },
        isExchange: Boolean(row.is_exchange),
        ...(row.linked_order_id ? { linkedOrderId: String(row.linked_order_id) } : {}),
        ...(row.customer_note ? { customerNote: String(row.customer_note) } : {}),
        rejectionNote: String(row.notes || ''),
        createdAt: new Date(row.created_at).toISOString(),
        updatedAt: new Date(row.updated_at).toISOString(),
        timeline: tlByRma.get(rmaId) || [],
      });
    }
  }

  private async hydrateCeilingsFromOrderServices(
    orderId: string,
    authHeader?: string,
    callerCustomerId?: string,
  ): Promise<void> {
    const orderUrl = process.env.ORDER_SERVICE_URL?.replace(/\/$/, '');
    const fulfillmentUrl = process.env.FULFILLMENT_SERVICE_URL?.replace(/\/$/, '');
    if (!orderUrl || !fulfillmentUrl) return;

    const headers: Record<string, string> = {};
    if (authHeader) {
      headers.Authorization = authHeader;
    }

    const [orderRes, trackingRes] = await Promise.all([
      fetch(`${orderUrl}/api/v1/orders/${encodeURIComponent(orderId)}`, { headers }),
      fetch(`${fulfillmentUrl}/api/v1/orders/${encodeURIComponent(orderId)}/tracking`, { headers }),
    ]);

    if (orderRes.status === 404 || orderRes.status === 403 || trackingRes.status === 404 || trackingRes.status === 403) {
      throw new NotFoundException(`Không tìm thấy đơn hàng ${orderId} của tài khoản hiện tại`);
    }
    if (!orderRes.ok || !trackingRes.ok) {
      throw new ServiceUnavailableException('Không thể xác minh thông tin đơn hàng từ order/fulfillment service');
    }

    const orderJson = (await orderRes.json()) as {
      data?: {
        customerId?: string;
        userId?: string;
        warehouseId?: string;
        shippingFeeVnd?: number;
        totalPayableVnd?: number;
        payment?: { capturedAmountVnd?: number };
        items?: Array<{
          skuId?: string;
          skuName?: string;
          quantity?: number;
          allocatedUnitPaidVnd?: number;
        }>;
      };
    };
    const trackingJson = (await trackingRes.json()) as {
      data?: {
        deliveredAt?: string;
        quantities?: Array<{ skuId?: string; delivered?: number }>;
        parcels?: Array<{
          warehouseId?: string;
          deliveredAt?: string;
          items?: Array<{ skuId?: string; deliveredQuantity?: number; quantity?: number }>;
        }>;
      };
    };

    const orderData = orderJson.data;
    const trackingData = trackingJson.data;
    if (!orderData?.items || !trackingData) {
      throw new NotFoundException(`Dữ liệu đơn hàng ${orderId} không đầy đủ`);
    }

    const orderOwner = orderData.customerId || orderData.userId;
    if (callerCustomerId && orderOwner && orderOwner !== callerCustomerId) {
      throw new ForbiddenException('Bạn không có quyền thao tác trả hàng trên đơn hàng này');
    }

    const deliveredBySku = new Map<string, number>();
    for (const q of trackingData.quantities || []) {
      if (q.skuId) {
        deliveredBySku.set(q.skuId, Number(q.delivered || 0));
      }
    }

    const parcelMetaBySku = new Map<string, { warehouseId?: string; deliveredAt?: string }>();
    for (const parcel of trackingData.parcels || []) {
      for (const pItem of parcel.items || []) {
        if (!pItem.skuId) continue;
        parcelMetaBySku.set(pItem.skuId, {
          warehouseId: parcel.warehouseId || orderData.warehouseId,
          deliveredAt: parcel.deliveredAt || trackingData.deliveredAt,
        });
      }
    }

    const acceptedQtyBySku = new Map<string, number>();
    const openQtyBySku = new Map<string, number>();
    for (const existing of this.returnCases.values()) {
      if (existing.orderId !== orderId || existing.status === 'REJECTED') continue;
      for (const it of existing.items) {
        if (existing.status === 'REFUNDED') {
          acceptedQtyBySku.set(
            it.skuId,
            (acceptedQtyBySku.get(it.skuId) || 0) + (it.qcPassedQuantity || it.quantity),
          );
        } else {
          openQtyBySku.set(it.skuId, (openQtyBySku.get(it.skuId) || 0) + it.quantity);
        }
      }
    }

    const parcelDelivered = (trackingData.parcels || [])
      .map((p) => p.deliveredAt)
      .filter((d): d is string => Boolean(d))
      .sort()
      .pop();
    const defaultDeliveredAt = trackingData.deliveredAt || parcelDelivered;
    const defaultWarehouseId =
      trackingData.parcels?.[0]?.warehouseId || orderData.warehouseId || 'WH-HCM-01';
    const shippingFeeVnd = Number(orderData.shippingFeeVnd ?? 0);

    for (const it of orderData.items) {
      if (!it.skuId) continue;
      const deliveredQty = deliveredBySku.get(it.skuId) ?? 0;
      if (deliveredQty <= 0) continue;
      const skuMeta = parcelMetaBySku.get(it.skuId);
      const deliveredAt = skuMeta?.deliveredAt || defaultDeliveredAt;
      if (!deliveredAt) {
        throw new ConflictException(`Kiện hàng cho SKU ${it.skuId} chưa có thời điểm giao thành công`);
      }
      this.seedCeiling({
        orderId,
        skuId: it.skuId,
        customerId: orderOwner,
        warehouseId: skuMeta?.warehouseId || defaultWarehouseId,
        deliveredQty,
        returnedAcceptedQty: acceptedQtyBySku.get(it.skuId) || 0,
        returnOpenQty: openQtyBySku.get(it.skuId) || 0,
        allocatedUnitPaidVnd: Number(it.allocatedUnitPaidVnd ?? 0),
        shippingFeeVnd,
        deliveredAt,
      });
    }

    const captured = Number(orderData.payment?.capturedAmountVnd ?? orderData.totalPayableVnd ?? 0);
    if (captured > 0 && !this.paymentClient.getSummary(orderId)) {
      this.paymentClient.setSummary(orderId, {
        capturedAmount: captured,
        refundedAmount: 0,
        refundPending: 0,
      });
    }
  }

  seedCeiling(ceiling: OrderLineCeiling): void {
    const key = `${ceiling.orderId}|${ceiling.skuId}`;
    this.ceilings.set(key, { ...ceiling });
  }

  getCeiling(orderId: string, skuId: string): OrderLineCeiling | undefined {
    return this.ceilings.get(`${orderId}|${skuId}`);
  }

  /**
   * Tạo yêu cầu trả hàng (RMA) tuân thủ B06, P07, P09, P11, I07 & SC07:
   * 1. Kiểm tra thời hạn 7 ngày từ ngày giao hàng.
   * 2. Kiểm tra bất biến I07: Số lượng trả không vượt quá delivered_qty - accepted - open.
   * 3. Giữ hạn mức số lượng (returnOpenQty += quantity).
   * 4. Tính toán số tiền hoàn dựa trên allocatedUnitPaidVnd lúc mua (P09 vs P11).
   */
  async openRmaRequest(
    dto: CreateReturnRequestDto,
    customerId = 'cust-default',
    authHeader?: string,
  ): Promise<ReturnCaseRecord> {
    await this.ensureHydrated();
    const rmaId = dto.rmaId || `RET-${Date.now()}-${Math.floor(Math.random() * 10000)}`;
    if (this.returnCases.has(rmaId)) {
      return this.returnCases.get(rmaId)!;
    }

    if (!dto.lines || dto.lines.length === 0) {
      throw new ConflictException('Yêu cầu trả hàng cần ít nhất 1 dòng sản phẩm');
    }

    if (process.env.ORDER_SERVICE_URL && process.env.FULFILLMENT_SERVICE_URL) {
      await this.hydrateCeilingsFromOrderServices(dto.orderId, authHeader, customerId);
    }

    // 1. Kiểm tra thời hạn 7 ngày (P07)
    const now = Date.now();
    const resolvedWarehouses = new Set<string>();
    for (const line of dto.lines) {
      const key = `${dto.orderId}|${line.skuId}`;
      const ceiling = this.ceilings.get(key);
      if (!ceiling) {
        throw new NotFoundException(`Không tìm thấy dòng hàng đã giao cho SKU ${line.skuId} của đơn ${dto.orderId}`);
      }
      if (ceiling.customerId && ceiling.customerId !== customerId) {
        throw new ForbiddenException('Bạn không có quyền tạo yêu cầu trả hàng cho đơn hàng này');
      }
      if (ceiling.warehouseId) {
        resolvedWarehouses.add(ceiling.warehouseId);
      }

      const deliveredTime = new Date(ceiling.deliveredAt).getTime();
      if (Number.isNaN(deliveredTime)) {
        throw new ConflictException(`Không xác định được thời gian giao hàng của SKU ${line.skuId}`);
      }

      if (now - deliveredTime > SEVEN_DAYS_MS) {
        throw new ConflictException(
          `Quá hạn đổi trả: Đơn hàng đã giao quá 7 ngày theo quy định B06 (${new Date(deliveredTime).toLocaleDateString()})`,
        );
      }

      // 2. Kiểm tra bất biến I07 (delivered >= accepted + open + requested)
      const remainingReturnable =
        ceiling.deliveredQty - ceiling.returnedAcceptedQty - ceiling.returnOpenQty;
      if (line.quantity <= 0 || line.quantity > remainingReturnable) {
        throw new ConflictException(
          `Vi phạm bất biến I07: Số lượng yêu cầu trả (${line.quantity}) vượt hạn mức còn được trả (${remainingReturnable}) của SKU ${line.skuId}`,
        );
      }
    }

    if (resolvedWarehouses.size > 1) {
      throw new ConflictException(
        'Vui lòng tạo yêu cầu trả hàng riêng cho từng kho xử lý của kiện hàng',
      );
    }
    const warehouseId = Array.from(resolvedWarehouses)[0] || 'WH-HCM-01';

    // 3. Khóa số lượng vào returnOpenQty để chặn 2 yêu cầu trùng lặp
    let goodsRefundVnd = 0;
    const items: ReturnCaseLine[] = [];
    let isFullOrderReturn = true;

    for (const line of dto.lines) {
      const key = `${dto.orderId}|${line.skuId}`;
      const ceiling = this.ceilings.get(key)!;
      ceiling.returnOpenQty += line.quantity;

      const lineRefund = line.quantity * ceiling.allocatedUnitPaidVnd;
      goodsRefundVnd += lineRefund;

      if (line.quantity < ceiling.deliveredQty) {
        isFullOrderReturn = false;
      }

      items.push({
        skuId: line.skuId,
        skuName: line.skuName || line.skuId,
        quantity: line.quantity,
        receivedQuantity: 0,
        qcPassedQuantity: 0,
        qcFailedQuantity: 0,
        allocatedUnitPaidVnd: ceiling.allocatedUnitPaidVnd,
      });
    }

    const orderCeilings = Array.from(this.ceilings.values()).filter(
      (c) => c.orderId === dto.orderId,
    );
    const requestedBySku = new Map(dto.lines.map((l) => [l.skuId, l.quantity]));
    if (
      orderCeilings.some(
        (c) => (requestedBySku.get(c.skuId) || 0) < c.deliveredQty,
      )
    ) {
      isFullOrderReturn = false;
    }

    // 4. Tính toán phí vận chuyển hoàn (P09 vs P11)
    const isSellerError = [
      'WRONG_ITEM',
      'DAMAGED',
      'DEFECTIVE',
      'NOT_AS_DESCRIBED',
      'SELLER_FAULT',
    ].includes(dto.reason);

    let shippingRefundVnd = 0;
    // P11: Lỗi do cửa hàng và trả toàn bộ đơn hàng -> hoàn lại cả phí ship (30.000đ)
    if (isSellerError && isFullOrderReturn) {
      const firstCeiling = this.ceilings.get(`${dto.orderId}|${dto.lines[0].skuId}`);
      shippingRefundVnd = firstCeiling?.shippingFeeVnd || 0;
    }

    const totalRefundableVnd = goodsRefundVnd + shippingRefundVnd;
    const nowIso = new Date().toISOString();

    const record: ReturnCaseRecord = {
      returnId: rmaId,
      orderId: dto.orderId,
      customerId,
      warehouseId,
      status: 'REQUESTED',
      reason: dto.reason,
      items,
      refundableAmountVnd: totalRefundableVnd,
      shippingRefundVnd,
      refund: {
        amountVnd: totalRefundableVnd,
        goodsVnd: goodsRefundVnd,
        shippingVnd: shippingRefundVnd,
        status: 'PENDING',
      },
      isExchange: Boolean(dto.isExchange),
      ...(dto.notes?.trim() ? { customerNote: dto.notes.trim() } : {}),
      rejectionNote: '',
      createdAt: nowIso,
      updatedAt: nowIso,
      timeline: [
        {
          at: nowIso,
          actor: customerId,
          note: `Khách hàng mở yêu cầu trả hàng (${dto.reason}): ${items.map((i) => `${i.quantity}x ${i.skuId}`).join(', ')}. Dự kiến hoàn: ${totalRefundableVnd.toLocaleString('vi-VN')} đ`,
        },
      ],
    };

    this.returnCases.set(rmaId, record);

    if (this.dbReady()) {
      await this.persistRma(record, record.timeline[0]);
    }

    return record;
  }

  private async persistRma(
    record: ReturnCaseRecord,
    timelineEntry?: { status?: string; actor: string; note: string; at: string },
  ): Promise<void> {
    if (!this.dbReady()) return;
    await this.db.query(
      `INSERT INTO support.rma_requests (
        rma_id, order_id, customer_id, warehouse_id, reason, status,
        refundable_amount_vnd, shipping_refund_vnd, is_exchange, linked_order_id, notes, customer_note, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
      ON CONFLICT (rma_id) DO UPDATE SET
        status = EXCLUDED.status,
        refundable_amount_vnd = EXCLUDED.refundable_amount_vnd,
        shipping_refund_vnd = EXCLUDED.shipping_refund_vnd,
        is_exchange = EXCLUDED.is_exchange,
        linked_order_id = EXCLUDED.linked_order_id,
        notes = EXCLUDED.notes,
        customer_note = COALESCE(EXCLUDED.customer_note, support.rma_requests.customer_note),
        updated_at = EXCLUDED.updated_at`,
      [
        record.returnId,
        record.orderId,
        record.customerId,
        record.warehouseId,
        record.reason,
        record.status,
        record.refundableAmountVnd,
        record.shippingRefundVnd,
        record.isExchange,
        record.linkedOrderId || null,
        record.rejectionNote || null,
        record.customerNote || null,
        record.createdAt,
        record.updatedAt,
      ],
    );

    for (const item of record.items) {
      const qcStatus =
        item.qcPassedQuantity > 0 ? 'PASS' : item.qcFailedQuantity > 0 ? 'FAIL' : 'PENDING';
      await this.db.query(
        `INSERT INTO support.rma_items (
          rma_id, sku_id, sku_name, quantity, unit_paid_vnd, qc_status, received_qty, qc_passed_qty, qc_failed_qty
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (rma_id, sku_id) DO UPDATE SET
          qc_status = EXCLUDED.qc_status,
          received_qty = EXCLUDED.received_qty,
          qc_passed_qty = EXCLUDED.qc_passed_qty,
          qc_failed_qty = EXCLUDED.qc_failed_qty`,
        [
          record.returnId,
          item.skuId,
          item.skuName,
          item.quantity,
          item.allocatedUnitPaidVnd,
          qcStatus,
          item.receivedQuantity,
          item.qcPassedQuantity,
          item.qcFailedQuantity,
        ],
      );
    }

    if (timelineEntry) {
      await this.db.query(
        `INSERT INTO support.rma_timeline (rma_id, status, actor, note, created_at)
         VALUES ($1, $2, $3, $4, $5)`,
        [
          record.returnId,
          timelineEntry.status || record.status,
          timelineEntry.actor,
          timelineEntry.note,
          timelineEntry.at,
        ],
      );
    }
  }

  async syncRecord(record: ReturnCaseRecord): Promise<void> {
    if (!this.dbReady()) return;
    const latest = record.timeline[record.timeline.length - 1];
    await this.persistRma(
      record,
      latest ? { status: record.status, ...latest } : undefined,
    );
  }

  getReturn(returnId: string): ReturnCaseRecord {
    const record = this.returnCases.get(returnId);
    if (!record) {
      throw new NotFoundException(`Không tìm thấy hồ sơ trả hàng ${returnId}`);
    }
    return record;
  }

  listReturns(filter: {
    orderId?: string;
    customerId?: string;
    status?: string;
    warehouseId?: string;
  }): ReturnCaseRecord[] {
    let result = Array.from(this.returnCases.values());

    if (filter.orderId) {
      result = result.filter((r) => r.orderId === filter.orderId);
    }
    if (filter.customerId) {
      result = result.filter((r) => r.customerId === filter.customerId);
    }
    if (filter.status) {
      result = result.filter((r) => r.status === filter.status);
    }
    if (filter.warehouseId) {
      result = result.filter((r) => r.warehouseId === filter.warehouseId);
    }

    return result.sort(
      (a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime(),
    );
  }

  /**
   * Hỗ trợ phê duyệt hồ sơ trả hàng bởi CSKH
   */
  approveReturn(returnId: string, actor: string): ReturnCaseRecord {
    const record = this.getReturn(returnId);
    if (record.status !== 'REQUESTED') {
      throw new ConflictException(`Không thể phê duyệt yêu cầu ở trạng thái ${record.status}`);
    }

    const nowIso = new Date().toISOString();
    record.status = 'APPROVED';
    record.updatedAt = nowIso;
    record.timeline.push({
      at: nowIso,
      actor,
      note: 'CSKH đã duyệt yêu cầu trả hàng, chờ khách gửi hàng về kho.',
    });

    return record;
  }

  private async syncInventoryReturnStep(
    path: '/api/v1/inventory/returns/receive' | '/api/v1/inventory/returns/qc-pass' | '/api/v1/inventory/returns/qc-fail',
    warehouseId: string,
    skuId: string,
    quantity: number,
  ): Promise<void> {
    if (quantity <= 0) return;
    const baseUrl = process.env.INVENTORY_SERVICE_URL?.trim().replace(/\/$/, '');
    if (!baseUrl) return;

    let token = '';
    try {
      token = resolveInternalServiceToken(false);
    } catch {
      token = process.env.INTERNAL_SERVICE_TOKEN || '';
    }
    if (!token) {
      throw new ServiceUnavailableException(
        'INTERNAL_SERVICE_TOKEN is required for inventory return sync',
      );
    }

    const res = await fetch(`${baseUrl}${path}`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Internal-Token': token,
      },
      body: JSON.stringify({ warehouseId, skuId, quantity }),
    });
    if (!res.ok) {
      const json = (await res.json().catch(() => ({}))) as { message?: string };
      throw new ConflictException(
        json.message || `Inventory service ${path} failed with ${res.status}`,
      );
    }
  }

  /**
   * Kho tiếp nhận kiện hàng trả (SC07: OnHand +qty, Unsellable +qty, Available giữ nguyên)
   */
  async receiveRma(returnId: string, actor: string): Promise<ReturnCaseRecord> {
    const record = this.getReturn(returnId);
    if (!['REQUESTED', 'APPROVED'].includes(record.status)) {
      throw new ConflictException(`Không thể nhận hàng trả ở trạng thái ${record.status}`);
    }

    for (const item of record.items) {
      await this.syncInventoryReturnStep(
        '/api/v1/inventory/returns/receive',
        record.warehouseId,
        item.skuId,
        item.quantity,
      );
    }

    const nowIso = new Date().toISOString();
    record.status = 'RECEIVED';
    record.updatedAt = nowIso;

    for (const item of record.items) {
      item.receivedQuantity = item.quantity;
    }

    record.timeline.push({
      at: nowIso,
      actor,
      note: `Kho đã tiếp nhận kiện hàng trả. Tồn kho chuyển vào xô Chờ kiểm định (Unsellable +${record.items.map((i) => i.quantity).reduce((a, b) => a + b, 0)}).`,
    });

    return record;
  }

  /**
   * Kho thẩm định chất lượng QC (SC07: Đạt -> Unsellable -qty, Available +qty)
   */
  async submitQcResult(
    returnId: string,
    results: Array<{ skuId: string; passedQuantity: number; failedQuantity: number }>,
    actor: string,
  ): Promise<ReturnCaseRecord> {
    const record = this.getReturn(returnId);
    if (record.status !== 'RECEIVED') {
      throw new ConflictException(`Chỉ có thể nhập kết quả QC cho hồ sơ đã nhận hàng (hiện tại: ${record.status})`);
    }

    if (!Array.isArray(results) || results.length === 0) {
      throw new BadRequestException('Thiếu kết quả kiểm định QC cho các sản phẩm trả hàng');
    }

    const bySku = new Map<string, { passedQuantity: number; failedQuantity: number }>();
    for (const res of results) {
      if (!res?.skuId || bySku.has(res.skuId)) {
        throw new BadRequestException(`Dữ liệu QC trùng lặp hoặc thiếu skuId (${res?.skuId || ''})`);
      }
      const item = record.items.find((i) => i.skuId === res.skuId);
      if (!item) {
        throw new BadRequestException(`SKU ${res.skuId} không thuộc hồ sơ trả hàng ${returnId}`);
      }
      const passed = Number(res.passedQuantity);
      const failed = Number(res.failedQuantity);
      if (
        !Number.isInteger(passed) ||
        !Number.isInteger(failed) ||
        passed < 0 ||
        failed < 0 ||
        passed + failed !== item.quantity
      ) {
        throw new BadRequestException(
          `Số lượng QC không hợp lệ cho ${item.skuId}: đạt (${passed}) + lỗi (${failed}) phải bằng ${item.quantity}`,
        );
      }
      bySku.set(res.skuId, { passedQuantity: passed, failedQuantity: failed });
    }

    for (const item of record.items) {
      if (!bySku.has(item.skuId)) {
        throw new BadRequestException(`Thiếu kết quả QC cho SKU ${item.skuId}`);
      }
    }

    for (const item of record.items) {
      const res = bySku.get(item.skuId)!;
      if (res.passedQuantity > 0) {
        await this.syncInventoryReturnStep(
          '/api/v1/inventory/returns/qc-pass',
          record.warehouseId,
          item.skuId,
          res.passedQuantity,
        );
      }
      if (res.failedQuantity > 0) {
        await this.syncInventoryReturnStep(
          '/api/v1/inventory/returns/qc-fail',
          record.warehouseId,
          item.skuId,
          res.failedQuantity,
        );
      }
    }

    let totalPassed = 0;
    let totalFailed = 0;
    let totalQty = 0;
    let passedGoodsRefundVnd = 0;

    for (const item of record.items) {
      const res = bySku.get(item.skuId)!;
      item.qcPassedQuantity = res.passedQuantity;
      item.qcFailedQuantity = res.failedQuantity;
      totalPassed += res.passedQuantity;
      totalFailed += res.failedQuantity;
      totalQty += item.quantity;
      passedGoodsRefundVnd += res.passedQuantity * item.allocatedUnitPaidVnd;
    }

    const effectiveShippingRefund =
      totalPassed === totalQty && record.shippingRefundVnd > 0 ? record.shippingRefundVnd : 0;
    const totalRefundable = passedGoodsRefundVnd + effectiveShippingRefund;
    record.shippingRefundVnd = effectiveShippingRefund;
    record.refundableAmountVnd = totalRefundable;
    record.refund = {
      amountVnd: totalRefundable,
      goodsVnd: passedGoodsRefundVnd,
      shippingVnd: effectiveShippingRefund,
      status: 'PENDING',
    };

    const nowIso = new Date().toISOString();
    record.updatedAt = nowIso;

    if (totalPassed > 0) {
      record.status = 'QC_PASSED';
      record.timeline.push({
        at: nowIso,
        actor,
        note: `Hoàn tất thẩm định QC: ${totalPassed} sản phẩm ĐẠT chuẩn (trả về bán tiếp), ${totalFailed} sản phẩm LỖI/HỎNG (cách ly tiêu hủy). Giá trị hoàn theo QC: ${totalRefundable.toLocaleString('vi-VN')} đ.`,
      });
    } else {
      record.status = 'QC_FAILED';
      for (const item of record.items) {
        const ceiling = this.ceilings.get(`${record.orderId}|${item.skuId}`);
        if (ceiling) {
          ceiling.returnOpenQty = Math.max(0, ceiling.returnOpenQty - item.quantity);
        }
      }
      record.timeline.push({
        at: nowIso,
        actor,
        note: `Hoàn tất thẩm định QC: Toàn bộ ${totalFailed} sản phẩm không đạt chuẩn chất lượng.`,
      });
    }

    return record;
  }

  /**
   * Từ chối yêu cầu trả hàng -> nhả lại hạn mức returnOpenQty (I07 an toàn)
   */
  rejectReturn(returnId: string, note: string, actor: string): ReturnCaseRecord {
    const record = this.getReturn(returnId);
    if (['REFUNDED', 'REJECTED'].includes(record.status)) {
      throw new ConflictException(`Hồ sơ đã kết thúc ở trạng thái ${record.status}`);
    }

    // Nhả lại returnOpenQty
    for (const item of record.items) {
      const key = `${record.orderId}|${item.skuId}`;
      const ceiling = this.ceilings.get(key);
      if (ceiling) {
        ceiling.returnOpenQty = Math.max(0, ceiling.returnOpenQty - item.quantity);
      }
    }

    const nowIso = new Date().toISOString();
    record.status = 'REJECTED';
    record.rejectionNote = note;
    record.updatedAt = nowIso;
    record.timeline.push({
      at: nowIso,
      actor,
      note: `Từ chối yêu cầu: ${note}. Đã giải phóng hạn mức trả hàng I07.`,
    });

    return record;
  }

  /**
   * Thực thi hoàn tiền tuân thủ I03 & SC08:
   * 1. Kiểm tra trạng thái hồ sơ.
   * 2. Khóa giữ trần hoàn tiền qua paymentClient.reserveRefundCeiling(orderId, refundableAmountVnd).
   * 3. Thực thi chuyển tiền qua paymentClient.executeRefundPayout(orderId, refundableAmountVnd).
   * 4. Cập nhật trạng thái REFUNDED và refund.status = 'COMPLETED'.
   */
  async disburseRefund(returnId: string, actor: string): Promise<ReturnCaseRecord> {
    const record = this.getReturn(returnId);
    if (record.refund?.status === 'COMPLETED' || record.status === 'REFUNDED') {
      throw new ConflictException('Hồ sơ trả hàng này đã được hoàn tiền');
    }

    if (!['QC_PASSED', 'REFUND_PENDING'].includes(record.status)) {
      throw new ConflictException(
        `Chỉ có thể hoàn tiền sau khi hoàn tất QC đạt chuẩn (trạng thái hiện tại: ${record.status})`,
      );
    }

    const totalPassed = record.items.reduce((sum, item) => sum + item.qcPassedQuantity, 0);
    const totalReturned = record.items.reduce((sum, item) => sum + item.quantity, 0);
    if (totalPassed <= 0) {
      throw new ConflictException('Không có sản phẩm nào đạt QC để thực hiện hoàn tiền');
    }

    const goodsVnd = record.items.reduce(
      (sum, item) => sum + item.qcPassedQuantity * item.allocatedUnitPaidVnd,
      0,
    );
    const shippingVnd =
      totalPassed === totalReturned && record.shippingRefundVnd > 0 ? record.shippingRefundVnd : 0;
    const amountVnd = goodsVnd + shippingVnd;
    if (amountVnd <= 0) {
      throw new ConflictException('Số tiền hoàn hợp lệ sau QC phải lớn hơn 0');
    }

    record.shippingRefundVnd = shippingVnd;
    record.refundableAmountVnd = amountVnd;

    // Ngưỡng phê duyệt hoàn tiền (P05/P10):
    // Nếu hoàn > 500.000đ: Bắt buộc phải có phiếu duyệt Maker-Checker đã APPROVED từ Quản lý
    if (amountVnd > 500_000) {
      const approval = await this.makerChecker.getApprovalByReference(record.returnId);
      if (!approval || approval.status !== 'APPROVED') {
        throw new ForbiddenException(
          `Hoàn tiền ${amountVnd.toLocaleString(
            'vi-VN',
          )} đ (> 500.000đ) cần được phê duyệt Maker-Checker bởi Quản lý trước khi chi trả`,
        );
      }
    }

    // 1. Khóa trần hoàn tiền (I03, SC08)
    await this.paymentClient.reserveRefundCeiling(record.orderId, amountVnd, record.returnId);

    // 2. Chuyển tiền qua đối tác / simulator
    await this.paymentClient.executeRefundPayout(record.orderId, amountVnd, record.returnId);

    for (const item of record.items) {
      const ceiling = this.ceilings.get(`${record.orderId}|${item.skuId}`);
      if (ceiling) {
        ceiling.returnedAcceptedQty += item.qcPassedQuantity;
        ceiling.returnOpenQty = Math.max(0, ceiling.returnOpenQty - item.quantity);
      }
    }

    const nowIso = new Date().toISOString();
    record.status = 'REFUNDED';
    record.updatedAt = nowIso;
    record.refund = {
      amountVnd,
      goodsVnd,
      shippingVnd,
      status: 'COMPLETED',
    };

    record.timeline.push({
      at: nowIso,
      actor,
      note: `Hoàn tất chi trả hoàn tiền ${amountVnd.toLocaleString(
        'vi-VN',
      )} đ cho khách hàng (I03/SC08).`,
    });

    await this.syncRecord(record);

    return record;
  }

  /**
   * Tạo yêu cầu phê duyệt Maker-Checker cho hoàn tiền giá trị lớn > 500.000đ (P05/P10)
   */
  async requestRefundApproval(returnId: string, makerUserId: string, reason?: string) {
    const record = this.getReturn(returnId);
    return this.makerChecker.createApprovalRequest({
      requestType: 'EXCEPTION_REFUND',
      referenceId: returnId,
      amountVnd: record.refundableAmountVnd,
      makerUserId,
      reason:
        reason ||
        `Yêu cầu duyệt chi hoàn tiền ${record.refundableAmountVnd.toLocaleString(
          'vi-VN',
        )} đ cho hồ sơ trả hàng ${returnId}`,
    });
  }

  /**
   * Nghiệp vụ Đổi hàng (Exchange = return + linked new order, B06):
   * Khách đổi sản phẩm: Sau khi nhận hàng & QC đạt (hoặc CSKH duyệt đổi),
   * hệ thống sinh một đơn hàng mới liên kết (linkedOrderId) thay vì sửa SKU/giá của đơn cũ.
   */
  async createLinkedExchangeOrder(
    returnId: string,
    replacementSkuId?: string,
    actor = 'support-agent',
  ): Promise<ReturnCaseRecord> {
    const record = this.getReturn(returnId);
    if (!['REQUESTED', 'APPROVED', 'RECEIVED', 'QC_PASSED'].includes(record.status)) {
      throw new ConflictException(`Không thể tạo đơn đổi hàng cho hồ sơ ở trạng thái ${record.status}`);
    }

    if (record.linkedOrderId) {
      throw new ConflictException(`Hồ sơ đổi hàng này đã được liên kết với đơn ${record.linkedOrderId}`);
    }

    const linkedOrderId = `O-EXCH-${record.returnId}`;
    const nowIso = new Date().toISOString();

    record.isExchange = true;
    record.linkedOrderId = linkedOrderId;
    record.status = 'QC_PASSED';
    record.updatedAt = nowIso;

    const targetSku = replacementSkuId || record.items[0]?.skuId || 'SKU-EXCHANGE';
    record.timeline.push({
      at: nowIso,
      actor,
      note: `Tạo đơn hàng đổi mới liên kết ${linkedOrderId} (sản phẩm đổi: ${targetSku}). Hồ sơ trả cũ giữ nguyên không sửa đổi dữ liệu gốc (B06).`,
    });

    if (this.db) {
      try {
        await this.db.query(
          `UPDATE support.rma_requests
           SET is_exchange = TRUE, linked_order_id = $1, status = $2, updated_at = $3
           WHERE rma_id = $4`,
          [linkedOrderId, record.status, nowIso, returnId],
        );
      } catch {
        // Fallback in-memory
      }
    }

    return record;
  }

  /**
   * Cập nhật bản ghi trả hàng nội bộ
   */
  updateRecord(record: ReturnCaseRecord): void {
    this.returnCases.set(record.returnId, record);
  }
}
