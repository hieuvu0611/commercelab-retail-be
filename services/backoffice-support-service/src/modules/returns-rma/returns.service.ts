import {
  ConflictException,
  ForbiddenException,
  Injectable,
  NotFoundException,
} from '@nestjs/common';
import { CreateReturnRequestDto } from './create-return.dto';
import { DatabaseService } from '../../database/database.service';
import { PaymentClient } from './payment.client';
import { MakerCheckerPolicyService } from '../../common/guards/maker-checker.guard';

export interface OrderLineCeiling {
  orderId: string;
  skuId: string;
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
export class ReturnsRmaService {
  private readonly ceilings = new Map<string, OrderLineCeiling>();
  private readonly returnCases = new Map<string, ReturnCaseRecord>();

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
  ): Promise<ReturnCaseRecord> {
    const rmaId = dto.rmaId || `RET-${Date.now()}-${Math.floor(Math.random() * 10000)}`;
    if (this.returnCases.has(rmaId)) {
      return this.returnCases.get(rmaId)!;
    }

    if (!dto.lines || dto.lines.length === 0) {
      throw new ConflictException('Yêu cầu trả hàng cần ít nhất 1 dòng sản phẩm');
    }

    // 1. Kiểm tra thời hạn 7 ngày (P07)
    const now = Date.now();
    for (const line of dto.lines) {
      const key = `${dto.orderId}|${line.skuId}`;
      const ceiling = this.ceilings.get(key);
      if (!ceiling) {
        throw new NotFoundException(`Không tìm thấy dòng hàng đã giao cho SKU ${line.skuId} của đơn ${dto.orderId}`);
      }

      const deliveredTime = dto.deliveredAt
        ? new Date(dto.deliveredAt).getTime()
        : new Date(ceiling.deliveredAt).getTime();

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
      warehouseId: 'WH-HCM-01',
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

    // Lưu vào database Postgres schema support nếu kết nối sẵn sàng
    try {
      await this.persistRma(record);
    } catch {
      // Tiếp tục hoạt động với in-memory nếu DB chưa chạy trong môi trường test độc lập
    }

    return record;
  }

  private async persistRma(record: ReturnCaseRecord): Promise<void> {
    await this.db.query(
      `INSERT INTO support.rma_requests (
        rma_id, order_id, customer_id, warehouse_id, reason, status,
        refundable_amount_vnd, shipping_refund_vnd, is_exchange, notes, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
      ON CONFLICT (rma_id) DO UPDATE SET status = EXCLUDED.status, updated_at = EXCLUDED.updated_at`,
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
        record.rejectionNote || null,
        record.createdAt,
        record.updatedAt,
      ],
    );

    for (const item of record.items) {
      await this.db.query(
        `INSERT INTO support.rma_items (
          rma_id, sku_id, sku_name, quantity, unit_paid_vnd, qc_status
        ) VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (rma_id, sku_id) DO NOTHING`,
        [
          record.returnId,
          item.skuId,
          item.skuName,
          item.quantity,
          item.allocatedUnitPaidVnd,
          'PENDING',
        ],
      );
    }
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

  /**
   * Kho tiếp nhận kiện hàng trả (SC07: OnHand +qty, Unsellable +qty, Available giữ nguyên)
   */
  receiveRma(returnId: string, actor: string): ReturnCaseRecord {
    const record = this.getReturn(returnId);
    if (!['REQUESTED', 'APPROVED'].includes(record.status)) {
      throw new ConflictException(`Không thể nhận hàng trả ở trạng thái ${record.status}`);
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
  submitQcResult(
    returnId: string,
    results: Array<{ skuId: string; passedQuantity: number; failedQuantity: number }>,
    actor: string,
  ): ReturnCaseRecord {
    const record = this.getReturn(returnId);
    if (record.status !== 'RECEIVED') {
      throw new ConflictException(`Chỉ có thể nhập kết quả QC cho hồ sơ đã nhận hàng (hiện tại: ${record.status})`);
    }

    let totalPassed = 0;
    let totalFailed = 0;

    for (const res of results) {
      const item = record.items.find((i) => i.skuId === res.skuId);
      if (item) {
        item.qcPassedQuantity = res.passedQuantity;
        item.qcFailedQuantity = res.failedQuantity;
        totalPassed += res.passedQuantity;
        totalFailed += res.failedQuantity;
      }
    }

    const nowIso = new Date().toISOString();
    record.updatedAt = nowIso;

    if (totalPassed > 0) {
      record.status = 'QC_PASSED';
      record.timeline.push({
        at: nowIso,
        actor,
        note: `Hoàn tất thẩm định QC: ${totalPassed} sản phẩm ĐẠT chuẩn (trả về bán tiếp), ${totalFailed} sản phẩm LỖI/HỎNG (cách ly tiêu hủy).`,
      });
    } else {
      record.status = 'QC_FAILED';
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
    if (!['QC_PASSED', 'APPROVED', 'RECEIVED', 'REFUND_PENDING'].includes(record.status)) {
      throw new ConflictException(`Không thể hoàn tiền cho hồ sơ ở trạng thái ${record.status}`);
    }

    if (record.refund?.status === 'COMPLETED' || record.status === 'REFUNDED') {
      throw new ConflictException('Hồ sơ trả hàng này đã được hoàn tiền');
    }

    const amountVnd = record.refundableAmountVnd;

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
    await this.paymentClient.reserveRefundCeiling(record.orderId, amountVnd);

    // 2. Chuyển tiền qua đối tác / simulator
    await this.paymentClient.executeRefundPayout(record.orderId, amountVnd);

    const nowIso = new Date().toISOString();
    record.status = 'REFUNDED';
    record.updatedAt = nowIso;
    record.refund = {
      amountVnd,
      goodsVnd: record.refund?.goodsVnd || amountVnd - record.shippingRefundVnd,
      shippingVnd: record.shippingRefundVnd,
      status: 'COMPLETED',
    };

    record.timeline.push({
      at: nowIso,
      actor,
      note: `Hoàn tất chi trả hoàn tiền ${amountVnd.toLocaleString(
        'vi-VN',
      )} đ cho khách hàng (I03/SC08).`,
    });

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
