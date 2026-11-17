import { ConflictException, Injectable } from '@nestjs/common';

export interface PaymentSummaryView {
  orderId: string;
  capturedAmount: number;
  refundedAmount: number;
  refundPending: number;
  remainingRefundable: number;
}

@Injectable()
export class PaymentClient {
  private readonly paymentSummaries = new Map<string, {
    capturedAmount: number;
    refundedAmount: number;
    refundPending: number;
  }>([
    // Baseline order O-001 (SC01 & SC07): 660,000 VND captured
    ['O-001', { capturedAmount: 660_000, refundedAmount: 0, refundPending: 0 }],
  ]);

  /**
   * Nạp số tiền thực thu từ Payment Service khi orderId chưa có trong cache nội bộ.
   */
  async hydrateOrderSummary(orderId: string): Promise<void> {
    if (this.paymentSummaries.has(orderId)) return;
    const baseUrl = process.env.PAYMENT_SERVICE_URL?.replace(/\/$/, '');
    const token = process.env.INTERNAL_SERVICE_TOKEN;
    if (!baseUrl || !token) return;
    const res = await fetch(
      `${baseUrl}/api/v1/payments/orders/${encodeURIComponent(orderId)}/attempt`,
      { headers: { 'X-Internal-Token': token } },
    );
    if (!res.ok) return;
    const json = (await res.json()) as {
      data?: { capturedAmountVnd?: number; amountVnd?: number; status?: string };
    };
    const captured = Number(json.data?.capturedAmountVnd ?? 0);
    const effective =
      captured > 0
        ? captured
        : json.data?.status === 'COLLECTED' || json.data?.status === 'CAPTURED'
          ? Number(json.data?.amountVnd ?? 0)
          : 0;
    if (effective > 0) {
      this.paymentSummaries.set(orderId, {
        capturedAmount: effective,
        refundedAmount: 0,
        refundPending: 0,
      });
    }
  }

  /**
   * Giữ chỗ trong trần hoàn tiền trước khi gọi đối tác chi trả (I03, SC08).
   * SC08: 660.000đ thực thu, 270.000đ đã hoàn, 300.000đ đang giữ -> còn 90.000đ; từ chối yêu cầu 100.000đ.
   */
  async reserveRefundCeiling(orderId: string, amountVnd: number): Promise<PaymentSummaryView> {
    await this.hydrateOrderSummary(orderId);
    const summary = this.paymentSummaries.get(orderId) || {
      capturedAmount: 660_000,
      refundedAmount: 0,
      refundPending: 0,
    };

    const remaining = summary.capturedAmount - summary.refundedAmount - summary.refundPending;
    if (amountVnd <= 0 || amountVnd > remaining) {
      throw new ConflictException(
        `Vượt trần hoàn tiền cho phép (I03, SC08): Yêu cầu hoàn ${amountVnd.toLocaleString(
          'vi-VN',
        )} đ vượt hạn mức còn lại (${remaining.toLocaleString('vi-VN')} đ)`,
      );
    }

    // Khóa ngay vào refundPending; timeout cũng KHÔNG được tự động nhả trần
    summary.refundPending += amountVnd;
    this.paymentSummaries.set(orderId, summary);

    return {
      orderId,
      capturedAmount: summary.capturedAmount,
      refundedAmount: summary.refundedAmount,
      refundPending: summary.refundPending,
      remainingRefundable: summary.capturedAmount - summary.refundedAmount - summary.refundPending,
    };
  }

  /**
   * Hoàn tất chi trả tiền hoàn qua Payment Simulator:
   * Chuyển từ refundPending sang refundedAmount.
   */
  async executeRefundPayout(orderId: string, amountVnd: number): Promise<PaymentSummaryView> {
    const summary = this.paymentSummaries.get(orderId);
    if (!summary || summary.refundPending < amountVnd) {
      throw new ConflictException('Không tìm thấy hạn mức hoàn tiền đang giữ chờ chi trả');
    }

    summary.refundPending -= amountVnd;
    summary.refundedAmount += amountVnd;
    this.paymentSummaries.set(orderId, summary);

    return {
      orderId,
      capturedAmount: summary.capturedAmount,
      refundedAmount: summary.refundedAmount,
      refundPending: summary.refundPending,
      remainingRefundable: summary.capturedAmount - summary.refundedAmount - summary.refundPending,
    };
  }

  getSummary(orderId: string): PaymentSummaryView | undefined {
    const s = this.paymentSummaries.get(orderId);
    if (!s) return undefined;
    return {
      orderId,
      capturedAmount: s.capturedAmount,
      refundedAmount: s.refundedAmount,
      refundPending: s.refundPending,
      remainingRefundable: s.capturedAmount - s.refundedAmount - s.refundPending,
    };
  }

  setSummary(orderId: string, summary: { capturedAmount: number; refundedAmount: number; refundPending: number }) {
    this.paymentSummaries.set(orderId, { ...summary });
  }
}
