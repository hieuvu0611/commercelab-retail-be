import { BadRequestException, ConflictException, ForbiddenException, NotFoundException } from '@nestjs/common';
import { MakerCheckerPolicyService } from '../maker-checker.guard';

describe('MakerCheckerPolicyService', () => {
  let service: MakerCheckerPolicyService;

  beforeEach(() => {
    service = new MakerCheckerPolicyService();
  });

  describe('Validation & Policy P10 (Maker != Checker)', () => {
    it('rejects when maker or checker is missing', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'EXCEPTION_REFUND',
          makerUserId: '',
          checkerUserId: 'user-2',
          checkerRole: 'CSKH_LEAD',
          amountVnd: 100000,
          quantityDiff: 0,
        }),
      ).toThrow(BadRequestException);
    });

    it('strictly rejects when maker equals checker (P10 invariant)', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'EXCEPTION_REFUND',
          makerUserId: 'user-1',
          checkerUserId: 'user-1',
          checkerRole: 'CSKH_LEAD',
          amountVnd: 100000,
          quantityDiff: 0,
        }),
      ).toThrow(ForbiddenException);
    });
  });

  describe('EXCEPTION_REFUND thresholds (00:22)', () => {
    it('allows exception refund <= 500,000 for CSKH_LEAD', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'EXCEPTION_REFUND',
          makerUserId: 'user-maker',
          checkerUserId: 'user-checker',
          checkerRole: 'CSKH_LEAD',
          amountVnd: 500000,
          quantityDiff: 0,
        }),
      ).not.toThrow();
    });

    it('rejects exception refund > 500,000 when approved by CSKH_LEAD', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'EXCEPTION_REFUND',
          makerUserId: 'user-maker',
          checkerUserId: 'user-checker',
          checkerRole: 'CSKH_LEAD',
          amountVnd: 500001,
          quantityDiff: 0,
        }),
      ).toThrow(ForbiddenException);
    });

    it('allows exception refund > 500,000 when approved by OPS_MGR or FINANCE_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'EXCEPTION_REFUND',
          makerUserId: 'user-maker',
          checkerUserId: 'user-checker',
          checkerRole: 'OPS_MGR',
          amountVnd: 1000000,
          quantityDiff: 0,
        }),
      ).not.toThrow();
    });
  });

  describe('STOCK_ADJUSTMENT thresholds (00:22)', () => {
    it('allows stock adjustment <= 5 units and <= 2,000,000 VND for WAREHOUSE_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'STOCK_ADJUSTMENT',
          makerUserId: 'wh-staff',
          checkerUserId: 'wh-manager',
          checkerRole: 'WAREHOUSE_MGR',
          amountVnd: 1500000,
          quantityDiff: -3,
        }),
      ).not.toThrow();
    });

    it('rejects stock adjustment > 5 units when approved by WAREHOUSE_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'STOCK_ADJUSTMENT',
          makerUserId: 'wh-staff',
          checkerUserId: 'wh-manager',
          checkerRole: 'WAREHOUSE_MGR',
          amountVnd: 1500000,
          quantityDiff: -6,
        }),
      ).toThrow(ForbiddenException);
    });

    it('rejects stock adjustment > 2,000,000 VND when approved by WAREHOUSE_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'STOCK_ADJUSTMENT',
          makerUserId: 'wh-staff',
          checkerUserId: 'wh-manager',
          checkerRole: 'WAREHOUSE_MGR',
          amountVnd: 2500000,
          quantityDiff: -2,
        }),
      ).toThrow(ForbiddenException);
    });

    it('allows large stock adjustment (> 5 units or > 2,000,000 VND) for SUPPLY_CHAIN_MGR or ADMIN', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'STOCK_ADJUSTMENT',
          makerUserId: 'wh-staff',
          checkerUserId: 'sc-manager',
          checkerRole: 'SUPPLY_CHAIN_MGR',
          amountVnd: 5000000,
          quantityDiff: -10,
        }),
      ).not.toThrow();
    });
  });

  describe('PURCHASE_ORDER thresholds (00:22)', () => {
    it('allows PO <= 20,000,000 VND for PROCUREMENT_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'PURCHASE_ORDER',
          makerUserId: 'proc-buyer',
          checkerUserId: 'proc-mgr',
          checkerRole: 'PROCUREMENT_MGR',
          amountVnd: 18000000,
          quantityDiff: 50,
        }),
      ).not.toThrow();
    });

    it('rejects PO > 20,000,000 VND when approved by PROCUREMENT_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'PURCHASE_ORDER',
          makerUserId: 'proc-buyer',
          checkerUserId: 'proc-mgr',
          checkerRole: 'PROCUREMENT_MGR',
          amountVnd: 25000000,
          quantityDiff: 100,
        }),
      ).toThrow(ForbiddenException);
    });

    it('allows PO > 20,000,000 VND when approved by OPS_DIRECTOR or FINANCE_MGR', () => {
      expect(() =>
        service.validateApproval({
          requestType: 'PURCHASE_ORDER',
          makerUserId: 'proc-buyer',
          checkerUserId: 'ops-director',
          checkerRole: 'OPS_DIRECTOR',
          amountVnd: 50000000,
          quantityDiff: 200,
        }),
      ).not.toThrow();
    });
  });

  describe('Lifecycle: create, get, list, decide', () => {
    it('creates and lists approvals correctly', async () => {
      const created = await service.createApprovalRequest({
        requestType: 'STOCK_ADJUSTMENT',
        referenceId: 'ST-001',
        amountVnd: 600000,
        quantityDiff: -2,
        makerUserId: 'staff-minh',
        reason: 'Shortage detected during stocktake',
      });

      expect(created.approvalId).toBeDefined();
      expect(created.status).toBe('PENDING');

      const item = service.getApproval(created.approvalId);
      expect(item).toBeDefined();
      expect(item?.referenceId).toBe('ST-001');

      const list = service.listApprovals({ status: 'PENDING', requestType: 'STOCK_ADJUSTMENT' });
      expect(list.length).toBe(1);
    });

    it('rejects self-approval on decideApproval', async () => {
      const created = await service.createApprovalRequest({
        requestType: 'EXCEPTION_REFUND',
        referenceId: 'RMA-001',
        amountVnd: 300000,
        makerUserId: 'staff-an',
        reason: 'Customer goodwill refund',
      });

      await expect(
        service.decideApproval(created.approvalId, 'staff-an', 'SUPPORT_LEAD', 'APPROVE'),
      ).rejects.toThrow(ForbiddenException);
    });

    it('successfully approves with second checker and prevents duplicate decide', async () => {
      const created = await service.createApprovalRequest({
        requestType: 'EXCEPTION_REFUND',
        referenceId: 'RMA-002',
        amountVnd: 300000,
        makerUserId: 'staff-an',
        reason: 'Customer goodwill refund',
      });

      const approved = await service.decideApproval(
        created.approvalId,
        'lead-binh',
        'SUPPORT_LEAD',
        'APPROVE',
        'Approved after reviewing photos',
      );

      expect(approved.status).toBe('APPROVED');
      expect(approved.checkerUserId).toBe('lead-binh');
      expect(approved.decisionNote).toBe('Approved after reviewing photos');

      // Attempting to decide again must throw ConflictException
      await expect(
        service.decideApproval(created.approvalId, 'lead-binh', 'SUPPORT_LEAD', 'REJECT'),
      ).rejects.toThrow(ConflictException);
    });

    it('throws NotFoundException on unknown approvalId', async () => {
      await expect(
        service.decideApproval('APP-UNKNOWN', 'checker', 'ADMIN', 'APPROVE'),
      ).rejects.toThrow(NotFoundException);
    });
  });
});
