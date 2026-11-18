import { BadRequestException, ForbiddenException, ValidationPipe } from '@nestjs/common';
import { AppController } from '../app.controller';
import { MakerCheckerPolicyService } from '../common/guards/maker-checker.guard';
import { PaymentClient } from '../modules/returns-rma/payment.client';
import { ReturnsRmaService } from '../modules/returns-rma/returns.service';
import { CreateOrderReturnRequestDto, CreateReturnRequestDto } from '../modules/returns-rma/create-return.dto';

describe('AppController security & policy enforcement (BE-T2, BE-T3, BE-T4)', () => {
  let controller: AppController;
  let returnsService: ReturnsRmaService;
  let makerChecker: MakerCheckerPolicyService;

  beforeEach(() => {
    const paymentClient = new PaymentClient();
    makerChecker = new MakerCheckerPolicyService();
    returnsService = new ReturnsRmaService(undefined as any, paymentClient, makerChecker);
    controller = new AppController(
      makerChecker,
      returnsService,
      undefined as any,
      undefined as any,
      undefined as any,
      undefined as any,
      undefined as any,
    );
  });

  it('accepts an order ID in the route and requires it on the general returns endpoint', async () => {
    const pipe = new ValidationPipe({ transform: true, whitelist: true, forbidNonWhitelisted: true });
    const payload = {
      reason: 'DEFECTIVE',
      lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
    };
    const body = await pipe.transform(payload, { type: 'body', metatype: CreateOrderReturnRequestDto });
    const result = await controller.createOrderReturn({ user: { userId: 'user-lan' } }, 'O-001', body);
    expect(result.data.orderId).toBe('O-001');
    await expect(pipe.transform(payload, { type: 'body', metatype: CreateReturnRequestDto }))
      .rejects.toThrow(BadRequestException);
  });

  it('blocks cross-customer IDOR on GET /api/v1/returns/:returnId and GET /api/v1/orders/:orderId/returns', async () => {
    const rma = await returnsService.openRmaRequest(
      {
        orderId: 'O-001',
        reason: 'CHANGED_MIND',
        lines: [{ skuId: 'SKU-BALO-BLACK-20L', quantity: 1 }],
      },
      'user-lan',
    );

    await expect(
      controller.getReturnDetail({ user: { userId: 'user-other', roles: ['CUSTOMER'], warehouses: [] } }, rma.returnId),
    ).rejects.toThrow(ForbiddenException);

    await expect(
      controller.listOrderReturns({ user: { userId: 'user-other', roles: ['CUSTOMER'], warehouses: [] } }, 'O-001'),
    ).rejects.toThrow(ForbiddenException);

    const own = await controller.getReturnDetail(
      { user: { userId: 'user-lan', roles: ['CUSTOMER'], warehouses: [] } },
      rma.returnId,
    );
    expect(own.data.returnId).toBe(rma.returnId);
  });

  it('filters backoffice returns by warehouse scope and blocks out-of-scope warehouse receive', async () => {
    returnsService.seedCeiling({
      orderId: 'O-HN',
      skuId: 'SKU-HN-1',
      warehouseId: 'WH-HN-01',
      deliveredQty: 1,
      returnedAcceptedQty: 0,
      returnOpenQty: 0,
      allocatedUnitPaidVnd: 100_000,
      shippingFeeVnd: 0,
      deliveredAt: new Date().toISOString(),
    });
    const hnRma = await returnsService.openRmaRequest(
      { orderId: 'O-HN', reason: 'DEFECTIVE', lines: [{ skuId: 'SKU-HN-1', quantity: 1 }] },
      'user-hn',
    );

    const hcmUser = { userId: 'staff-hcm', roles: ['WAREHOUSE_STAFF'], warehouses: ['WH-HCM-01'] };
    const listRes = await controller.listBackofficeReturns({ user: hcmUser });
    expect(listRes.data.some((r) => r.returnId === hnRma.returnId)).toBe(false);

    await expect(controller.receiveReturnParcel({ user: hcmUser }, hnRma.returnId)).rejects.toThrow(
      ForbiddenException,
    );
  });

  it('evaluates full roles array in decideApproval', async () => {
    const app = await makerChecker.createApprovalRequest({
      requestType: 'EXCEPTION_REFUND',
      referenceId: 'RET-HIGH',
      amountVnd: 900_000,
      makerUserId: 'agent-1',
      reason: 'High value exception',
    });

    const decided = await controller.decideApproval(
      { user: { userId: 'mgr-1', roles: ['SUPPORT_AGENT', 'FINANCE_MGR'], warehouses: ['ALL'] } },
      app.approvalId,
      'APPROVE',
      'Verified receipts',
    );
    expect(decided.data.status).toBe('APPROVED');
    expect(decided.data.checkerRole).toBe('FINANCE_MGR');
  });
});
