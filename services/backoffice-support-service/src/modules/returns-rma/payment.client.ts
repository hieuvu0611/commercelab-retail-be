import {
  ConflictException,
  Injectable,
  ServiceUnavailableException,
} from '@nestjs/common';
import { resolveInternalServiceToken } from '../../common/guards/jwt-auth.guard';

export interface PaymentSummaryView {
  orderId: string;
  capturedAmount: number;
  refundedAmount: number;
  refundPending: number;
  remainingRefundable: number;
}

@Injectable()
export class PaymentClient {
  private readonly paymentSummaries = new Map<
    string,
    {
      capturedAmount: number;
      refundedAmount: number;
      refundPending: number;
    }
  >();

  constructor() {
    // Seed unit-test fixture only when running standalone without PAYMENT_SERVICE_URL
    if (!this.isRemoteEnabled() && !process.env.DATABASE_URL_SUPPORT) {
      this.paymentSummaries.set('O-001', {
        capturedAmount: 660_000,
        refundedAmount: 0,
        refundPending: 0,
      });
    }
  }

  private isRemoteEnabled(): boolean {
    return Boolean(process.env.PAYMENT_SERVICE_URL?.trim());
  }

  private remoteBaseUrl(): string {
    return (process.env.PAYMENT_SERVICE_URL || '').trim().replace(/\/$/, '');
  }

  private internalHeaders(): Record<string, string> {
    let token = '';
    try {
      token = resolveInternalServiceToken(false);
    } catch {
      token = process.env.INTERNAL_SERVICE_TOKEN || '';
    }
    if (!token) {
      throw new ServiceUnavailableException(
        'INTERNAL_SERVICE_TOKEN is required for payment-service calls',
      );
    }
    return {
      'Content-Type': 'application/json',
      'X-Internal-Token': token,
    };
  }

  private toView(
    orderId: string,
    summary: { capturedAmount: number; refundedAmount: number; refundPending: number },
  ): PaymentSummaryView {
    return {
      orderId,
      capturedAmount: summary.capturedAmount,
      refundedAmount: summary.refundedAmount,
      refundPending: summary.refundPending,
      remainingRefundable:
        summary.capturedAmount - summary.refundedAmount - summary.refundPending,
    };
  }

  private parseRemoteSummary(
    orderId: string,
    raw: Record<string, any> | undefined,
  ): PaymentSummaryView {
    const capturedAmount = Number(
      raw?.capturedAmount ?? raw?.capturedVnd ?? raw?.capturedAmountVnd ?? 0,
    );
    const refundedAmount = Number(
      raw?.refundedAmount ?? raw?.refundedVnd ?? raw?.refundedAmountVnd ?? 0,
    );
    const refundPending = Number(
      raw?.refundPending ?? raw?.refundPendingVnd ?? 0,
    );
    const entry = { capturedAmount, refundedAmount, refundPending };
    this.paymentSummaries.set(orderId, entry);
    return this.toView(orderId, entry);
  }

  /**
   * Reads authoritative payment summary from payment-service without fabricating defaults.
   */
  async hydrateOrderSummary(orderId: string): Promise<void> {
    if (!this.isRemoteEnabled()) return;
    const baseUrl = this.remoteBaseUrl();
    const headers = this.internalHeaders();

    const summaryRes = await fetch(
      `${baseUrl}/api/v1/payments/orders/${encodeURIComponent(orderId)}/summary`,
      { headers },
    );
    if (summaryRes.ok) {
      const json = (await summaryRes.json()) as { data?: Record<string, any> };
      this.parseRemoteSummary(orderId, json.data);
      return;
    }

    const attemptRes = await fetch(
      `${baseUrl}/api/v1/payments/orders/${encodeURIComponent(orderId)}/attempt`,
      { headers },
    );
    if (!attemptRes.ok) return;
    const json = (await attemptRes.json()) as {
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
   * Reserves refund ceiling persistently in payment-service (I03, SC08).
   */
  async reserveRefundCeiling(
    orderId: string,
    amountVnd: number,
    referenceId?: string,
  ): Promise<PaymentSummaryView> {
    if (this.isRemoteEnabled()) {
      const baseUrl = this.remoteBaseUrl();
      const res = await fetch(
        `${baseUrl}/api/v1/payments/${encodeURIComponent(orderId)}/refund-reservations`,
        {
          method: 'POST',
          headers: this.internalHeaders(),
          body: JSON.stringify({ amountVnd, referenceId }),
        },
      );
      const json = (await res.json().catch(() => ({}))) as {
        message?: string;
        data?: Record<string, any>;
      };
      if (res.status === 409 || res.status === 400) {
        throw new ConflictException(
          json.message ||
            `Vượt trần hoàn tiền cho phép (I03, SC08) cho đơn ${orderId}`,
        );
      }
      if (!res.ok) {
        throw new ServiceUnavailableException(
          json.message || `Payment service refund-reservations failed (${res.status})`,
        );
      }
      return this.parseRemoteSummary(orderId, json.data);
    }

    const summary = this.paymentSummaries.get(orderId);
    if (!summary) {
      throw new ConflictException(
        `Không tìm thấy bản ghi thực thu thanh toán cho đơn ${orderId}`,
      );
    }

    const remaining =
      summary.capturedAmount - summary.refundedAmount - summary.refundPending;
    if (amountVnd <= 0 || amountVnd > remaining) {
      throw new ConflictException(
        `Vượt trần hoàn tiền cho phép (I03, SC08): Yêu cầu hoàn ${amountVnd.toLocaleString(
          'vi-VN',
        )} đ vượt hạn mức còn lại (${remaining.toLocaleString('vi-VN')} đ)`,
      );
    }

    summary.refundPending += amountVnd;
    this.paymentSummaries.set(orderId, summary);
    return this.toView(orderId, summary);
  }

  /**
   * Completes refund payout in payment-service (refund_pending -> refunded_amount, status DONE).
   */
  async executeRefundPayout(
    orderId: string,
    amountVnd: number,
    referenceId?: string,
  ): Promise<PaymentSummaryView> {
    if (this.isRemoteEnabled()) {
      const baseUrl = this.remoteBaseUrl();
      const res = await fetch(
        `${baseUrl}/api/v1/payments/${encodeURIComponent(orderId)}/refund-payouts`,
        {
          method: 'POST',
          headers: this.internalHeaders(),
          body: JSON.stringify({ amountVnd, referenceId }),
        },
      );
      const json = (await res.json().catch(() => ({}))) as {
        message?: string;
        data?: Record<string, any>;
      };
      if (res.status === 409 || res.status === 400) {
        throw new ConflictException(
          json.message ||
            `Không thể hoàn tất chi trả tiền hoàn cho đơn ${orderId}`,
        );
      }
      if (!res.ok) {
        throw new ServiceUnavailableException(
          json.message || `Payment service refund-payouts failed (${res.status})`,
        );
      }
      return this.parseRemoteSummary(orderId, json.data);
    }

    const summary = this.paymentSummaries.get(orderId);
    if (!summary || summary.refundPending < amountVnd) {
      throw new ConflictException('Không tìm thấy hạn mức hoàn tiền đang giữ chờ chi trả');
    }

    summary.refundPending -= amountVnd;
    summary.refundedAmount += amountVnd;
    this.paymentSummaries.set(orderId, summary);
    return this.toView(orderId, summary);
  }

  getSummary(orderId: string): PaymentSummaryView | undefined {
    const s = this.paymentSummaries.get(orderId);
    if (!s) return undefined;
    return this.toView(orderId, s);
  }

  setSummary(
    orderId: string,
    summary: { capturedAmount: number; refundedAmount: number; refundPending: number },
  ) {
    this.paymentSummaries.set(orderId, { ...summary });
  }
}
