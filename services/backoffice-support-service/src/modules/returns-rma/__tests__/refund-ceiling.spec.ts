import { ConflictException } from '@nestjs/common';
import { ReturnsRmaService } from '../returns.service';
import { PaymentClient } from '../payment.client';
import { MakerCheckerPolicyService } from '../../../common/guards/maker-checker.guard';
import { DatabaseService } from '../../../database/database.service';

describe('Refund Ceiling & Execution (P8-T04, I03, SC08)', () => {
  let service: ReturnsRmaService;
  let paymentClient: PaymentClient;
  let makerChecker: MakerCheckerPolicyService;
  let mockDb: any;

  beforeEach(() => {
    mockDb = {
      query: jest.fn().mockResolvedValue({ rows: [], rowCount: 1 }),
      transaction: (work: (client: typeof mockDb) => Promise<unknown>) => work(mockDb),
      ping: jest.fn().mockResolvedValue(true),
    };
    paymentClient = new PaymentClient();
    makerChecker = new MakerCheckerPolicyService(mockDb as DatabaseService);
    service = new ReturnsRmaService(mockDb as DatabaseService, paymentClient, makerChecker);
  });

  it('enforces exact SC08 math: 660k captured, 270k refunded, 300k pending -> rejects 100k, allows 90k', async () => {
    // 1. Initial order O-001 has 660,000 VND captured
    // 2. Disburse first refund: 270,000 VND
    await paymentClient.reserveRefundCeiling('O-001', 270_000);
    await paymentClient.executeRefundPayout('O-001', 270_000);

    let summary = paymentClient.getSummary('O-001')!;
    expect(summary.refundedAmount).toBe(270_000);
    expect(summary.remainingRefundable).toBe(390_000);

    // 3. Hold an unresolved refund: 300,000 VND
    await paymentClient.reserveRefundCeiling('O-001', 300_000);
    summary = paymentClient.getSummary('O-001')!;
    expect(summary.refundPending).toBe(300_000);
    expect(summary.remainingRefundable).toBe(90_000);

    // 4. SC08: New request for 100,000 VND must be strictly rejected
    await expect(paymentClient.reserveRefundCeiling('O-001', 100_000)).rejects.toThrow(
      ConflictException,
    );

    // 5. Request within remaining ceiling (90,000 VND) must succeed
    const res = await paymentClient.reserveRefundCeiling('O-001', 90_000);
    expect(res.remainingRefundable).toBe(0);
    expect(res.refundPending).toBe(390_000);
  });

  it('disburses refund for QC-passed RMA and prevents double disbursement (I03)', async () => {
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
    });

    await service.receiveRma(rma.returnId, 'wh-staff');
    await service.submitQcResult(
      rma.returnId,
      [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 1, failedQuantity: 0 }],
      'qc-staff',
    );

    // Disburse refund: 270,000 VND
    const refunded = await service.disburseRefund(rma.returnId, 'finance-lead');
    expect(refunded.status).toBe('REFUNDED');
    expect(refunded.refund?.status).toBe('COMPLETED');
    expect(refunded.refund?.amountVnd).toBe(270_000);

    // Calling refund again on already refunded RMA must throw ConflictException
    await expect(service.disburseRefund(rma.returnId, 'finance-lead')).rejects.toThrow(
      ConflictException,
    );
  });

  it('rejects refund before QC and scales refund to QC-passed units only (SC07)', async () => {
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'DEFECTIVE',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 2 }],
    });

    await service.receiveRma(rma.returnId, 'wh-staff');
    await expect(service.disburseRefund(rma.returnId, 'finance-lead')).rejects.toThrow(
      ConflictException,
    );

    await service.submitQcResult(
      rma.returnId,
      [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 1, failedQuantity: 1 }],
      'qc-staff',
    );

    const refunded = await service.disburseRefund(rma.returnId, 'finance-lead');
    expect(refunded.status).toBe('REFUNDED');
    expect(refunded.refund?.amountVnd).toBe(270_000);
    expect(refunded.refundableAmountVnd).toBe(270_000);
  });

  it('does not fabricate a 660,000 VND summary for unknown orders', async () => {
    await expect(paymentClient.reserveRefundCeiling('O-UNKNOWN', 50_000)).rejects.toThrow(
      ConflictException,
    );
  });
});
