import { BadRequestException, ForbiddenException } from '@nestjs/common';
import { MakerCheckerPolicyService } from '../maker-checker.guard';

describe('MakerCheckerPolicyService', () => {
  let service: MakerCheckerPolicyService;

  beforeEach(() => {
    service = new MakerCheckerPolicyService();
  });

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

  it('strictly rejects when maker equals checker (P10)', () => {
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

  it('allows exception refund > 500,000 when approved by OPS_MGR', () => {
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
