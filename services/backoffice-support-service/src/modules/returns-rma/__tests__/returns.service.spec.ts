import { BadRequestException, ConflictException, NotFoundException } from '@nestjs/common';
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
      query: jest.fn().mockResolvedValue({ rows: [], rowCount: 1 }),
      transaction: (work: (client: typeof mockDb) => Promise<unknown>) => work(mockDb),
      ping: jest.fn().mockResolvedValue(true),
    };
    paymentClient = new PaymentClient();
    makerChecker = new MakerCheckerPolicyService(mockDb as DatabaseService);
    service = new ReturnsRmaService(mockDb as DatabaseService, paymentClient, makerChecker);
  });

  it('leaves an RMA and its quantity ceiling unchanged when the database transaction fails', async () => {
    await service.ensureHydrated();
    mockDb.query.mockRejectedValueOnce(new Error('write failed'));
    const skuId = 'SKU-BALO-BLACK-20L';
    const dto = {
      rmaId: 'RMA-WRITE-FAIL', orderId: 'O-001', reason: 'DEFECTIVE', lines: [{ skuId, quantity: 2 }],
    };
    await expect(service.openRmaRequest(dto)).rejects.toThrow('write failed');
    expect(service.listReturns({ orderId: dto.orderId })).toEqual([]);
    expect(service.getCeiling(dto.orderId, skuId)?.returnOpenQty).toBe(0);
    await expect(service.openRmaRequest(dto)).resolves.toMatchObject({ returnId: dto.rmaId });
    expect(service.getCeiling(dto.orderId, skuId)?.returnOpenQty).toBe(2);
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

  it('rejects duplicate SKU lines before reserving a return ceiling', async () => {
    const line = { skuId: 'SKU-BALO-BLACK-20L', quantity: 2 };
    await expect(service.openRmaRequest({
      orderId: 'O-001', reason: 'DAMAGED', lines: [line, line],
    })).rejects.toThrow(BadRequestException);
    expect(service.getCeiling('O-001', line.skuId)?.returnOpenQty).toBe(0);
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
    const received = await service.receiveRma(rma.returnId, 'wh-staff-1');
    expect(received.status).toBe('RECEIVED');
    expect(received.items[0].receivedQuantity).toBe(1);

    // Unbalanced QC must fail validation
    await expect(
      service.submitQcResult(
        rma.returnId,
        [{ skuId: 'SKU-BALO-BLACK-20L', passedQuantity: 2, failedQuantity: 0 }],
        'qc-staff-1',
      ),
    ).rejects.toThrow(BadRequestException);

    // 2. Warehouse QC inspects and passes
    const qcPassed = await service.submitQcResult(
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
    const prevInternalToken = process.env.INTERNAL_SERVICE_TOKEN;
    process.env.INTERNAL_SERVICE_TOKEN = "test-internal-token";
    process.env.ORDER_SERVICE_URL = 'http://orders.test:8083';
    process.env.FULFILLMENT_SERVICE_URL = 'http://fulfillment.test:8085';

    const nowIso = new Date().toISOString();
    global.fetch = jest.fn(async (url: any) => {
      const target = String(url);
      if (target.includes('/api/v1/shipments/internal/orders/O-REAL-2P')) {
        return Response.json({ data: [
          { warehouseId: 'WH-HCM-01', deliveredAt: nowIso, items: [
            { skuId: 'SKU-CAMPUS-BACKPACK-BLACK-20L', deliveredQuantity: 3 },
            { skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', deliveredQuantity: 1 },
          ] },
          { warehouseId: 'WH-HN-01', deliveredAt: nowIso, items: [
            { skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', deliveredQuantity: 1 },
          ] },
        ] });
      }
      return new Response(
        JSON.stringify({
          data: {
            orderId: 'O-REAL-2P',
            customerId: 'user-lan',
            shippingFeeVnd: 0,
            totalPayableVnd: 1_163_750,
            payment: { capturedAmountVnd: 1_163_750 },
            items: [
              { skuId: 'SKU-CAMPUS-BACKPACK-BLACK-20L', skuName: 'Campus', quantity: 3, allocatedUnitPaidVnd: 242_060 },
              { skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', skuName: 'Commuter', quantity: 2, allocatedUnitPaidVnd: 437_570 },
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
          notes: 'Handle snapped on arrival',
        },
        'user-lan',
        'Bearer test-jwt',
      );
      expect(rma.status).toBe('REQUESTED');
      expect(rma.warehouseId).toBe('WH-HCM-01');
      const second = await service.openRmaRequest({
        orderId: 'O-REAL-2P', reason: 'DAMAGED',
        lines: [{ skuId: 'SKU-COMMUTER-BACKPACK-GREY-28L', quantity: 1 }],
      }, 'user-lan', 'Bearer test-jwt');
      expect(second.warehouseId).toBe('WH-HN-01');
      expect(rma.customerNote).toBe('Handle snapped on arrival');
      expect(rma.refundableAmountVnd).toBe(437_570);
      expect(rma.shippingRefundVnd).toBe(0);
      expect(paymentClient.getSummary('O-REAL-2P')?.capturedAmount).toBe(1_163_750);
    } finally {
      global.fetch = prevFetch;
      if (prevInternalToken === undefined) delete process.env.INTERNAL_SERVICE_TOKEN;
      else process.env.INTERNAL_SERVICE_TOKEN = prevInternalToken;
      if (prevOrderUrl === undefined) delete process.env.ORDER_SERVICE_URL;
      else process.env.ORDER_SERVICE_URL = prevOrderUrl;
      if (prevFulfillmentUrl === undefined) delete process.env.FULFILLMENT_SERVICE_URL;
      else process.env.FULFILLMENT_SERVICE_URL = prevFulfillmentUrl;
    }
  });
});
