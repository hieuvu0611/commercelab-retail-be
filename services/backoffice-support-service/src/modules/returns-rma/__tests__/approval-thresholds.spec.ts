import { ForbiddenException } from '@nestjs/common';
import { ReturnsRmaService } from '../returns.service';
import { PaymentClient } from '../payment.client';
import { MakerCheckerPolicyService } from '../../../common/guards/maker-checker.guard';
import { DatabaseService } from '../../../database/database.service';

describe('Shipping Refund Rules & Approval Thresholds (P8-T05, P09, P11, P10)', () => {
  let service: ReturnsRmaService;
  let paymentClient: PaymentClient;
  let makerChecker: MakerCheckerPolicyService;
  let mockDb: any;

  beforeEach(() => {
    mockDb = {
      query: jest.fn().mockImplementation(async (sql: string, params: unknown[]) => ({
        rows: sql.includes('RETURNING approval_id') ? [{ approval_id: params[4] }] : [],
      })),
      ping: jest.fn().mockResolvedValue(true),
    };
    paymentClient = new PaymentClient();
    makerChecker = new MakerCheckerPolicyService(mockDb as DatabaseService);
    service = new ReturnsRmaService(mockDb as DatabaseService, paymentClient, makerChecker);
  });

  it('allows direct refund disbursement <= 500,000 VND without secondary approval', async () => {
    // Return 1 backpack: 270,000 VND (<= 500k)
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

    const refunded = await service.disburseRefund(rma.returnId, 'support-lead');
    expect(refunded.status).toBe('REFUNDED');
    expect(refunded.refund?.amountVnd).toBe(270_000);
  });

  it('blocks direct refund > 500,000 VND until Maker-Checker is approved (P10)', async () => {
    // Return 2 backpacks: 2 * 270,000 = 540,000 VND (> 500k)
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 2 }],
    });

    await service.receiveRma(rma.returnId, 'wh-staff');
    await service.submitQcResult(
      rma.returnId,
      [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 2, failedQuantity: 0 }],
      'qc-staff',
    );

    // 1. Direct disbursement without approval must be forbidden
    await expect(service.disburseRefund(rma.returnId, 'support-agent')).rejects.toThrow(
      ForbiddenException,
    );

    // 2. Request approval
    const approval = await service.requestRefundApproval(
      rma.returnId,
      'support-agent',
      'Refund 2 units',
    );
    expect(approval.status).toBe('PENDING');

    // 3. Maker cannot approve their own request (Maker != Checker, P10)
    await expect(
      makerChecker.decideApproval(
        approval.approvalId,
        'support-agent',
        'ADMIN',
        'APPROVE',
      ),
    ).rejects.toThrow(ForbiddenException);

    // 4. Authorized manager approves
    await makerChecker.decideApproval(
      approval.approvalId,
      'manager-ops',
      'WAREHOUSE_MGR',
      'APPROVE',
    );
    expect((await makerChecker.getApprovalByReference(rma.returnId))?.status).toBe('APPROVED');

    // 5. Now refund disbursement succeeds
    const refunded = await service.disburseRefund(rma.returnId, 'finance-agent');
    expect(refunded.status).toBe('REFUNDED');
    expect(refunded.refund?.amountVnd).toBe(540_000);
  });
});
