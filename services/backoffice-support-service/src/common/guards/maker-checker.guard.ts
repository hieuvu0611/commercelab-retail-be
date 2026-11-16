import { BadRequestException, ConflictException, ForbiddenException, Injectable, NotFoundException } from '@nestjs/common';
import { DatabaseService } from '../../database/database.service';

export type ApprovalType = 'EXCEPTION_REFUND' | 'STOCK_ADJUSTMENT' | 'PURCHASE_ORDER';

export interface ApprovalCandidate {
  requestType: ApprovalType;
  makerUserId: string;
  checkerUserId: string;
  checkerRole: string;
  amountVnd: number;
  quantityDiff: number;
}

export interface ApprovalRecord {
  approvalId: string;
  requestType: ApprovalType;
  referenceId: string;
  amountVnd: number;
  quantityDiff: number;
  makerUserId: string;
  checkerUserId?: string;
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'EXECUTED';
  reason: string;
  createdAt: string;
  updatedAt: string;
}

const SUPPORT_LEAD_ROLES = ['CSKH_LEAD', 'SUPPORT_LEAD', 'OPS_MGR', 'FINANCE_MGR', 'ADMIN'];
const MANAGER_ROLES = ['OPS_MGR', 'FINANCE_MGR', 'WAREHOUSE_MGR', 'ADMIN'];

@Injectable()
export class MakerCheckerPolicyService {
  private readonly approvals = new Map<string, ApprovalRecord>();

  constructor(private readonly db?: DatabaseService) {}

  /**
   * Thực thi Policy P10:
   * 1. Người tạo yêu cầu nhạy cảm KHÔNG ĐƯỢC tự duyệt (Maker != Checker).
   * 2. Ngưỡng phê duyệt hoàn tiền:
   *    - EXCEPTION_REFUND <= 500.000đ: SUPPORT_LEAD / CSKH_LEAD / Quản lý.
   *    - EXCEPTION_REFUND > 500.000đ: Bắt buộc OPS_MGR, FINANCE_MGR hoặc ADMIN.
   */
  validateApproval(input: ApprovalCandidate): void {
    if (!input.makerUserId || !input.checkerUserId) {
      throw new BadRequestException('Thiếu thông tin người đề nghị (Maker) hoặc người duyệt (Checker)');
    }
    if (input.makerUserId === input.checkerUserId) {
      throw new ForbiddenException(
        'Vi phạm Policy P10: Người tạo yêu cầu không được phép tự phê duyệt (Maker != Checker)',
      );
    }

    if (input.requestType === 'EXCEPTION_REFUND') {
      if (input.amountVnd <= 500_000) {
        if (!SUPPORT_LEAD_ROLES.includes(input.checkerRole)) {
          throw new ForbiddenException('Hoàn tiền ngoại lệ <= 500.000đ cần tối thiểu Support Lead phê duyệt');
        }
      } else if (!MANAGER_ROLES.includes(input.checkerRole)) {
        throw new ForbiddenException('Hoàn tiền ngoại lệ > 500.000đ bắt buộc cần Quản lý (Ops/Finance) phê duyệt');
      }
    }

    if (input.requestType === 'STOCK_ADJUSTMENT') {
      const withinWarehouseMgrLimit = Math.abs(input.quantityDiff) <= 5 && input.amountVnd <= 2_000_000;
      if (withinWarehouseMgrLimit) {
        if (!['WAREHOUSE_MGR', 'SUPPLY_CHAIN_MGR', 'ADMIN'].includes(input.checkerRole)) {
          throw new ForbiddenException('Điều chỉnh lệch kho cần tối thiểu WAREHOUSE_MGR phê duyệt');
        }
      } else if (!['SUPPLY_CHAIN_MGR', 'ADMIN'].includes(input.checkerRole)) {
        throw new ForbiddenException('Điều chỉnh lệch kho lớn cần SUPPLY_CHAIN_MGR hoặc ADMIN phê duyệt');
      }
    }

    if (input.requestType === 'PURCHASE_ORDER') {
      if (input.amountVnd <= 20_000_000) {
        if (!['PROCUREMENT_MGR', 'OPS_DIRECTOR', 'FINANCE_MGR', 'ADMIN'].includes(input.checkerRole)) {
          throw new ForbiddenException('Đơn mua hàng PO cần tối thiểu PROCUREMENT_MGR phê duyệt');
        }
      } else if (!['OPS_DIRECTOR', 'FINANCE_MGR', 'ADMIN'].includes(input.checkerRole)) {
        throw new ForbiddenException('Đơn mua hàng lớn > 20.000.000đ cần Ban giám đốc/Tài chính phê duyệt');
      }
    }
  }

  async createApprovalRequest(data: {
    requestType: ApprovalType;
    referenceId: string;
    amountVnd: number;
    quantityDiff?: number;
    makerUserId: string;
    reason: string;
  }): Promise<ApprovalRecord> {
    const approvalId = `APP-${Date.now()}-${Math.floor(Math.random() * 1000)}`;
    const nowIso = new Date().toISOString();

    const record: ApprovalRecord = {
      approvalId,
      requestType: data.requestType,
      referenceId: data.referenceId,
      amountVnd: data.amountVnd,
      quantityDiff: data.quantityDiff || 0,
      makerUserId: data.makerUserId,
      status: 'PENDING',
      reason: data.reason,
      createdAt: nowIso,
      updatedAt: nowIso,
    };

    this.approvals.set(approvalId, record);

    if (this.db) {
      try {
        await this.db.query(
          `INSERT INTO support.approval_requests (
            approval_id, request_type, reference_id, amount_vnd, quantity_diff,
            maker_user_id, status, reason, created_at
          ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
          ON CONFLICT (approval_id) DO NOTHING`,
          [
            record.approvalId,
            record.requestType,
            record.referenceId,
            record.amountVnd,
            record.quantityDiff,
            record.makerUserId,
            record.status,
            record.reason,
            record.createdAt,
          ],
        );
      } catch {
        // Fallback in-memory
      }
    }

    return record;
  }

  async decideApproval(
    approvalId: string,
    checkerUserId: string,
    checkerRole: string,
    action: 'APPROVE' | 'REJECT',
    note?: string,
  ): Promise<ApprovalRecord> {
    const record = this.approvals.get(approvalId);
    if (!record) {
      throw new NotFoundException(`Không tìm thấy yêu cầu phê duyệt ${approvalId}`);
    }
    if (record.status !== 'PENDING') {
      throw new ConflictException(`Yêu cầu phê duyệt này đã được xử lý (trạng thái: ${record.status})`);
    }

    // Kiểm tra chính sách P10
    this.validateApproval({
      requestType: record.requestType,
      makerUserId: record.makerUserId,
      checkerUserId,
      checkerRole,
      amountVnd: record.amountVnd,
      quantityDiff: record.quantityDiff,
    });

    const nowIso = new Date().toISOString();
    record.status = action === 'APPROVE' ? 'APPROVED' : 'REJECTED';
    record.checkerUserId = checkerUserId;
    record.updatedAt = nowIso;
    if (note) {
      record.reason = `${record.reason} | Note: ${note}`;
    }

    if (this.db) {
      try {
        await this.db.query(
          `UPDATE support.approval_requests
           SET status = $1, checker_user_id = $2
           WHERE approval_id = $3`,
          [record.status, checkerUserId, approvalId],
        );
      } catch {
        // Fallback in-memory
      }
    }

    return record;
  }

  listApprovals(filter?: { status?: string; requestType?: string }): ApprovalRecord[] {
    let list = Array.from(this.approvals.values());
    if (filter?.status) {
      list = list.filter((a) => a.status === filter.status);
    }
    if (filter?.requestType) {
      list = list.filter((a) => a.requestType === filter.requestType);
    }
    return list.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
  }

  getApprovalByReference(referenceId: string): ApprovalRecord | undefined {
    return Array.from(this.approvals.values()).find((a) => a.referenceId === referenceId);
  }
}
