import { ConflictException } from '@nestjs/common';
import { ReturnsRmaService } from '../returns.service';
import { PaymentClient } from '../payment.client';
import { MakerCheckerPolicyService } from '../../../common/guards/maker-checker.guard';
import { DatabaseService } from '../../../database/database.service';

describe('Exchange Orders (P8-T06, B06)', () => {
  let service: ReturnsRmaService;
  let paymentClient: PaymentClient;
  let makerChecker: MakerCheckerPolicyService;
  let mockDb: any;

  beforeEach(() => {
    mockDb = {
      query: jest.fn().mockResolvedValue({ rows: [] }),
      ping: jest.fn().mockResolvedValue(true),
    };
    paymentClient = new PaymentClient();
    makerChecker = new MakerCheckerPolicyService(mockDb as DatabaseService);
    service = new ReturnsRmaService(mockDb as DatabaseService, paymentClient, makerChecker);
  });

  it('links a new exchange order without mutating old order lines (B06)', async () => {
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'DEFECTIVE',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
      isExchange: true,
    });

    service.receiveRma(rma.returnId, 'wh-staff');
    service.submitQcResult(
      rma.returnId,
      [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 1, failedQuantity: 0 }],
      'qc-staff',
    );

    const exchanged = await service.createLinkedExchangeOrder(
      rma.returnId,
      'SKU-BALO-RED-20L',
      'support-agent',
    );

    expect(exchanged.isExchange).toBe(true);
    expect(exchanged.linkedOrderId).toBe(`O-EXCH-${rma.returnId}`);
    expect(exchanged.items[0].skuId).toBe('SKU-BALO-BLACK-20L'); // original SKU unchanged

    // Repeated call throws ConflictException
    await expect(
      service.createLinkedExchangeOrder(rma.returnId, 'SKU-BALO-RED-20L', 'support-agent'),
    ).rejects.toThrow(ConflictException);
  });
});
