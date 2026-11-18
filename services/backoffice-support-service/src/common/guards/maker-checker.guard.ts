import { BadRequestException, ConflictException, ForbiddenException, Injectable, NotFoundException } from '@nestjs/common';
import { DatabaseService } from '../../database/database.service';

export type ApprovalType = 'EXCEPTION_REFUND' | 'STOCK_ADJUSTMENT' | 'PURCHASE_ORDER';

export interface ApprovalCandidate {
  requestType: ApprovalType;
  makerUserId: string;
  checkerUserId: string;
  checkerRole: string | string[];
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
  checkerRole?: string;
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'EXECUTED';
  reason: string;
  decisionNote?: string;
  createdAt: string;
  updatedAt: string;
}

const SUPPORT_LEAD_ROLES = ['CSKH_LEAD', 'SUPPORT_LEAD', 'OPS_MGR', 'FINANCE_MGR', 'ADMIN'];
const MANAGER_ROLES = ['OPS_MGR', 'FINANCE_MGR', 'WAREHOUSE_MGR', 'ADMIN'];

@Injectable()
export class MakerCheckerPolicyService {
  private readonly approvals = new Map<string, ApprovalRecord>();

  constructor(private readonly db?: DatabaseService) {}

  private resolveQualifyingRole(
    rolesInput: string | string[],
    allowedRoles: readonly string[],
  ): string | undefined {
    const roles = Array.isArray(rolesInput) ? rolesInput : [rolesInput];
    return roles.find((role) => role && allowedRoles.includes(role));
  }

  /**
   * Thực thi Policy P10:
   * 1. Người tạo yêu cầu nhạy cảm KHÔNG ĐƯỢC tự duyệt (Maker != Checker).
   * 2. Ngưỡng phê duyệt hoàn tiền:
   *    - EXCEPTION_REFUND <= 500.000đ: SUPPORT_LEAD / CSKH_LEAD / Quản lý.
   *    - EXCEPTION_REFUND > 500.000đ: Bắt buộc OPS_MGR, FINANCE_MGR hoặc ADMIN.
   */
  validateApproval(input: ApprovalCandidate): string {
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
        const matched = this.resolveQualifyingRole(input.checkerRole, SUPPORT_LEAD_ROLES);
        if (!matched) {
          throw new ForbiddenException('Hoàn tiền ngoại lệ <= 500.000đ cần tối thiểu Support Lead phê duyệt');
        }
        return matched;
      }
      const matched = this.resolveQualifyingRole(input.checkerRole, MANAGER_ROLES);
      if (!matched) {
        throw new ForbiddenException('Hoàn tiền ngoại lệ > 500.000đ bắt buộc cần Quản lý (Ops/Finance) phê duyệt');
      }
      return matched;
    }

    if (input.requestType === 'STOCK_ADJUSTMENT') {
      const withinWarehouseMgrLimit = Math.abs(input.quantityDiff) <= 5 && input.amountVnd <= 2_000_000;
      if (withinWarehouseMgrLimit) {
        const matched = this.resolveQualifyingRole(input.checkerRole, ['WAREHOUSE_MGR', 'SUPPLY_CHAIN_MGR', 'OPS_MGR', 'ADMIN']);
        if (!matched) {
          throw new ForbiddenException('Điều chỉnh lệch kho cần tối thiểu WAREHOUSE_MGR phê duyệt');
        }
        return matched;
      }
      const matched = this.resolveQualifyingRole(input.checkerRole, ['SUPPLY_CHAIN_MGR', 'OPS_MGR', 'FINANCE_MGR', 'ADMIN']);
      if (!matched) {
        throw new ForbiddenException('Điều chỉnh lệch kho lớn cần SUPPLY_CHAIN_MGR, OPS_MGR, FINANCE_MGR hoặc ADMIN phê duyệt');
      }
      return matched;
    }

    if (input.requestType === 'PURCHASE_ORDER') {
      if (input.amountVnd <= 20_000_000) {
        const matched = this.resolveQualifyingRole(input.checkerRole, ['PROCUREMENT_MGR', 'OPS_DIRECTOR', 'OPS_MGR', 'FINANCE_MGR', 'ADMIN']);
        if (!matched) {
          throw new ForbiddenException('Đơn mua hàng PO cần tối thiểu PROCUREMENT_MGR phê duyệt');
        }
        return matched;
      }
      const matched = this.resolveQualifyingRole(input.checkerRole, ['OPS_DIRECTOR', 'OPS_MGR', 'FINANCE_MGR', 'ADMIN']);
      if (!matched) {
        throw new ForbiddenException('Đơn mua hàng lớn > 20.000.000đ cần Ban giám đốc/Tài chính phê duyệt');
      }
      return matched;
    }

    const roles = Array.isArray(input.checkerRole) ? input.checkerRole : [input.checkerRole];
    return roles[0] || '';
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

    const db = this.approvalDatabase();
    if (db) {
      await db.query(
        `INSERT INTO support.approval_requests (
          approval_id, request_type, reference_id, amount_vnd, quantity_diff,
          maker_user_id, status, reason, created_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
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
    } else {
      this.approvals.set(approvalId, record);
    }

    return record;
  }

  async decideApproval(
    approvalId: string,
    checkerUserId: string,
    checkerRole: string | string[],
    action: 'APPROVE' | 'REJECT',
    note?: string,
  ): Promise<ApprovalRecord> {
    const record = await this.getApproval(approvalId);
    if (!record) {
      throw new NotFoundException(`Approval request ${approvalId} not found`);
    }
    if (record.status !== 'PENDING') {
      throw new ConflictException(`Approval request already processed (status: ${record.status})`);
    }

    // Policy P10 validation
    const matchedRole = this.validateApproval({
      requestType: record.requestType,
      makerUserId: record.makerUserId,
      checkerUserId,
      checkerRole,
      amountVnd: record.amountVnd,
      quantityDiff: record.quantityDiff,
    });

    const decided: ApprovalRecord = {
      ...record,
      status: action === 'APPROVE' ? 'APPROVED' : 'REJECTED',
      checkerUserId,
      checkerRole: matchedRole,
      decisionNote: note || '',
      updatedAt: new Date().toISOString(),
      reason: note ? `${record.reason} | Note: ${note}` : record.reason,
    };

    const db = this.approvalDatabase();
    if (db) {
      const result = await db.query(
        `UPDATE support.approval_requests
         SET status = $1, checker_user_id = $2, decision_note = $3, updated_at = $4
         WHERE approval_id = $5 AND status = 'PENDING'
         RETURNING approval_id`,
        [decided.status, checkerUserId, decided.decisionNote, decided.updatedAt, approvalId],
      );
      if (result.rows.length === 0) {
        throw new ConflictException('Approval request already processed');
      }
    } else {
      if (this.approvals.get(approvalId)?.status !== 'PENDING') {
        throw new ConflictException('Approval request already processed');
      }
      this.approvals.set(approvalId, decided);
    }

    return decided;
  }

  private mapRow(row: any): ApprovalRecord {
    return {
      approvalId: row.approval_id,
      requestType: row.request_type,
      referenceId: row.reference_id,
      amountVnd: Number(row.amount_vnd || 0),
      quantityDiff: Number(row.quantity_diff || 0),
      makerUserId: row.maker_user_id,
      checkerUserId: row.checker_user_id ?? undefined,
      status: row.status,
      reason: row.reason,
      decisionNote: row.decision_note ?? undefined,
      createdAt: new Date(row.created_at).toISOString(),
      updatedAt: new Date(row.updated_at || row.created_at).toISOString(),
    };
  }

  private approvalDatabase(): DatabaseService | undefined {
    if (typeof this.db?.getPool === 'function' && !process.env.DATABASE_URL_SUPPORT) {
      return undefined;
    }
    return this.db;
  }

  async getApproval(approvalId: string): Promise<ApprovalRecord | undefined> {
    const db = this.approvalDatabase();
    if (!db) {
      return this.approvals.get(approvalId);
    }
    const res = await db.query(
      'SELECT * FROM support.approval_requests WHERE approval_id = $1',
      [approvalId],
    );
    return res.rows[0] ? this.mapRow(res.rows[0]) : undefined;
  }

  async listApprovals(filter?: { status?: string; requestType?: string; makerUserId?: string }): Promise<ApprovalRecord[]> {
    let list: ApprovalRecord[];
    const db = this.approvalDatabase();
    if (!db) {
      list = Array.from(this.approvals.values());
    } else {
      const res = await db.query(
        'SELECT * FROM support.approval_requests ORDER BY created_at DESC',
      );
      list = res.rows.map((r) => this.mapRow(r));
    }
    if (filter?.status) {
      list = list.filter((a) => a.status === filter.status);
    }
    if (filter?.requestType) {
      list = list.filter((a) => a.requestType === filter.requestType);
    }
    if (filter?.makerUserId) {
      list = list.filter((a) => a.makerUserId === filter.makerUserId);
    }
    return list.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
  }

  async getApprovalByReference(referenceId: string): Promise<ApprovalRecord | undefined> {
    const db = this.approvalDatabase();
    if (!db) {
      return Array.from(this.approvals.values()).find((a) => a.referenceId === referenceId);
    }
    const res = await db.query(
      'SELECT * FROM support.approval_requests WHERE reference_id = $1 ORDER BY created_at DESC LIMIT 1',
      [referenceId],
    );
    return res.rows[0] ? this.mapRow(res.rows[0]) : undefined;
  }
}
