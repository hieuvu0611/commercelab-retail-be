import { ConflictException, NotFoundException } from '@nestjs/common';
import { ReturnsRmaService } from '../returns.service';
import { PaymentClient } from '../payment.client';
import { MakerCheckerPolicyService } from '../../../common/guards/maker-checker.guard';
import { DatabaseService } from '../../../database/database.service';

describe('ReturnsRmaService (P8-T02, B06, I07, SC07)', () => {
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

  it('creates an RMA request within 7 days and calculates SC07 refund amount for change of mind (P09)', async () => {
    const res = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
    });

    expect(res.status).toBe('REQUESTED');
    expect(res.items).toHaveLength(1);
    expect(res.items[0].quantity).toBe(1);
    expect(res.items[0].allocatedUnitPaidVnd).toBe(270_000);
    // P09: Change of mind does not refund shipping (30,000)
    expect(res.shippingRefundVnd).toBe(0);
    expect(res.refundableAmountVnd).toBe(270_000);
    expect(res.refund?.amountVnd).toBe(270_000);

    // Ceiling returnOpenQty should now be 1
    const ceiling = service.getCeiling('O-001', 'SKU-BALO-BLACK-20L');
    expect(ceiling?.returnOpenQty).toBe(1);
  });

  it('strictly enforces I07: rejects return quantity exceeding delivered ceiling', async () => {
    // Ceiling delivered is 2, asking for 3 must fail
    await expect(
      service.openRmaRequest({
        orderId: 'O-001',
        reason: 'CHANGED_MIND',
        lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 3 }],
      }),
    ).rejects.toThrow(ConflictException);

    // First return 2
    await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 2 }],
    });

    // Consecutive return for 1 more must fail because 2 are already open
    await expect(
      service.openRmaRequest({
        orderId: 'O-001',
        reason: 'CHANGED_MIND',
        lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
      }),
    ).rejects.toThrow(ConflictException);
  });

  it('strictly rejects return request after 7-day return window has passed', async () => {
    const eightDaysAgo = new Date(Date.now() - 8 * 24 * 3600 * 1000).toISOString();
    service.seedCeiling({
      orderId: 'O-OLD',
      skuId: 'SKU-BALO-BLACK-20L',
      deliveredQty: 1,
      returnedAcceptedQty: 0,
      returnOpenQty: 0,
      allocatedUnitPaidVnd: 270_000,
      shippingFeeVnd: 30_000,
      deliveredAt: eightDaysAgo,
    });

    await expect(
      service.openRmaRequest({
        orderId: 'O-OLD',
        reason: 'CHANGED_MIND',
        lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
      }),
    ).rejects.toThrow(ConflictException);
  });

  it('refunds shipping fee under P11 when full order is returned due to seller error', async () => {
    const res = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'DEFECTIVE',
      lines: [
        { skuId: 'SKU-BALO-BLACK-20L', quantity: 2 },
        { skuId: 'SKU-BOTTLE-BLUE-500ML', quantity: 1 },
      ],
    });

    // Goods: 2*270,000 + 1*90,000 = 630,000. Shipping: 30,000. Total = 660,000 VND
    expect(res.shippingRefundVnd).toBe(30_000);
    expect(res.refundableAmountVnd).toBe(660_000);
  });

  it('releases returnOpenQty when support rejects return request', async () => {
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
    });

    expect(service.getCeiling('O-001', 'SKU-BALO-BLACK-20L')?.returnOpenQty).toBe(1);

    const rejected = service.rejectReturn(rma.returnId, 'Customer cancelled', 'support-user');
    expect(rejected.status).toBe('REJECTED');
    expect(service.getCeiling('O-001', 'SKU-BALO-BLACK-20L')?.returnOpenQty).toBe(0);
  });

  it('handles warehouse receive and QC inspection workflow (SC07)', async () => {
    const rma = await service.openRmaRequest({
      orderId: 'O-001',
      reason: 'CHANGED_MIND',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
    });

    // 1. Warehouse receives package
    const received = service.receiveRma(rma.returnId, 'wh-staff-1');
    expect(received.status).toBe('RECEIVED');
    expect(received.items[0].receivedQuantity).toBe(1);

    // 2. Warehouse QC inspects and passes
    const qcPassed = service.submitQcResult(
      rma.returnId,
      [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 1, failedQuantity: 0 }],
      'qc-staff-1',
    );
    expect(qcPassed.status).toBe('QC_PASSED');
    expect(qcPassed.items[0].qcPassedQuantity).toBe(1);
    expect(qcPassed.items[0].qcFailedQuantity).toBe(0);
  });

  it('hydrates delivered multi-parcel order ceilings dynamically from order and fulfillment services', async () => {
    const prevOrderUrl = process.env.ORDER_SERVICE_URL;
    const prevFulfillmentUrl = process.env.FULFILLMENT_SERVICE_URL;
    const prevFetch = global.fetch;
    process.env.ORDER_SERVICE_URL = 'http://orders.test:8083';
    process.env.FULFILLMENT_SERVICE_URL = 'http://fulfillment.test:8085';

    const nowIso = new Date().toISOString();
    global.fetch = jest.fn(async (url: any) => {
      const target = String(url);
      if (target.includes('/api/v1/orders/O-REAL-2P/tracking')) {
        return new Response(
          JSON.stringify({
            data: {
              orderId: 'O-REAL-2P',
              status: 'DELIVERED',
              quantities: [
                { skuId: 'SKU-CAMPUS-BACKPACK-BLACK-20L', ordered: 3, delivered: 3 },
                { skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', ordered: 1, delivered: 1 },
              ],
              parcels: [
                { shipmentId: 'SHP-O-REAL-2P-1', deliveredAt: nowIso },
                { shipmentId: 'SHP-O-REAL-2P-2', deliveredAt: nowIso },
              ],
            },
          }),
          { status: 200 },
        );
      }
      return new Response(
        JSON.stringify({
          data: {
            orderId: 'O-REAL-2P',
            shippingFeeVnd: 0,
            totalPayableVnd: 1_163_750,
            payment: { capturedAmountVnd: 1_163_750 },
            items: [
              { skuId: 'SKU-CAMPUS-BACKPACK-BLACK-20L', skuName: 'Campus', quantity: 3, allocatedUnitPaidVnd: 242_060 },
              { skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', skuName: 'Commuter', quantity: 1, allocatedUnitPaidVnd: 437_570 },
            ],
          },
        }),
        { status: 200 },
      );
    }) as any;

    try {
      const rma = await service.openRmaRequest(
        {
          orderId: 'O-REAL-2P',
          reason: 'DAMAGED',
          lines: [{ skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', quantity: 1 }],
        },
        'user-lan',
        'Bearer test-jwt',
      );
      expect(rma.status).toBe('REQUESTED');
      expect(rma.refundableAmountVnd).toBe(437_570);
      expect(rma.shippingRefundVnd).toBe(0);
      expect(paymentClient.getSummary('O-REAL-2P')?.capturedAmount).toBe(1_163_750);
    } finally {
      global.fetch = prevFetch;
      if (prevOrderUrl === undefined) delete process.env.ORDER_SERVICE_URL;
      else process.env.ORDER_SERVICE_URL = prevOrderUrl;
      if (prevFulfillmentUrl === undefined) delete process.env.FULFILLMENT_SERVICE_URL;
      else process.env.FULFILLMENT_SERVICE_URL = prevFulfillmentUrl;
    }
  });
});
