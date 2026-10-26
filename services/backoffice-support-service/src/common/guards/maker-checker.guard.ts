import { BadRequestException, ForbiddenException, Injectable } from '@nestjs/common';

export type ApprovalType = 'EXCEPTION_REFUND' | 'STOCK_ADJUSTMENT' | 'PURCHASE_ORDER';

export interface ApprovalCandidate {
  requestType: ApprovalType;
  makerUserId: string;
  checkerUserId: string;
  checkerRole: string;
  amountVnd: number;
  quantityDiff: number;
}

@Injectable()
export class MakerCheckerPolicyService {
  /**
   * Thực thi Policy P10 & Bảng quyết định tạm chốt Mục 1.7:
   * 1. Người tạo yêu cầu nhạy cảm KHÔNG ĐƯỢC tự duyệt (Maker != Checker).
   * 2. Kiểm tra ngưỡng phê duyệt theo số tiền / số lượng:
   *    - EXCEPTION_REFUND: <= 500.000đ (CSKH_LEAD | OPS_MGR | FINANCE_MGR), > 500.000đ (OPS_MGR | FINANCE_MGR)
   *    - STOCK_ADJUSTMENT: <= 5 chiếc & <= 2.000.000đ (WAREHOUSE_MGR), lớn hơn cần SUPPLY_CHAIN_MGR
   *    - PURCHASE_ORDER: <= 20.000.000đ (PROCUREMENT_MGR), lớn hơn cần OPS_DIRECTOR | FINANCE_MGR
   */
  validateApproval(input: ApprovalCandidate): void {
    if (!input.makerUserId || !input.checkerUserId) {
      throw new BadRequestException('Thiếu thông tin người đề nghị (Maker) hoặc người duyệt (Checker)');
    }
    if (input.makerUserId === input.checkerUserId) {
      throw new ForbiddenException('Vi phạm Policy P10: Người tạo yêu cầu không được phép tự phê duyệt (Maker != Checker)');
    }

    if (input.requestType === 'EXCEPTION_REFUND') {
      if (input.amountVnd <= 500_000) {
        if (!['CSKH_LEAD', 'OPS_MGR', 'FINANCE_MGR'].includes(input.checkerRole)) {
          throw new ForbiddenException('Hoàn tiền ngoại lệ <= 500.000đ cần tối thiểu CSKH_LEAD phê duyệt');
        }
      } else if (!['OPS_MGR', 'FINANCE_MGR'].includes(input.checkerRole)) {
        throw new ForbiddenException('Hoàn tiền ngoại lệ > 500.000đ bắt buộc cần OPS_MGR hoặc FINANCE_MGR phê duyệt');
      }
    }

    if (input.requestType === 'STOCK_ADJUSTMENT') {
      const withinWarehouseMgrLimit = Math.abs(input.quantityDiff) <= 5 && input.amountVnd <= 2_000_000;
      if (withinWarehouseMgrLimit) {
        if (!['WAREHOUSE_MGR', 'SUPPLY_CHAIN_MGR'].includes(input.checkerRole)) {
          throw new ForbiddenException('Điều chỉnh lệch kho cần tối thiểu WAREHOUSE_MGR phê duyệt');
        }
      } else if (input.checkerRole !== 'SUPPLY_CHAIN_MGR') {
        throw new ForbiddenException('Điều chỉnh lệch kho > 5 chiếc hoặc > 2.000.000đ cần SUPPLY_CHAIN_MGR phê duyệt');
      }
    }

    if (input.requestType === 'PURCHASE_ORDER') {
      if (input.amountVnd <= 20_000_000) {
        if (!['PROCUREMENT_MGR', 'OPS_DIRECTOR', 'FINANCE_MGR'].includes(input.checkerRole)) {
          throw new ForbiddenException('Đơn mua hàng PO <= 20.000.000đ cần tối thiểu PROCUREMENT_MGR phê duyệt');
        }
      } else if (!['OPS_DIRECTOR', 'FINANCE_MGR'].includes(input.checkerRole)) {
        throw new ForbiddenException('Đơn mua hàng PO > 20.000.000đ cần OPS_DIRECTOR hoặc FINANCE_MGR phê duyệt');
      }
    }
  }
}
