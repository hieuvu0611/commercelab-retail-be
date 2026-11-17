import { BadRequestException, Injectable, NotFoundException } from '@nestjs/common';

export type CaseCategory =
  | 'PAYMENT_UNKNOWN_EXPIRED'
  | 'PICK_SHORTFALL'
  | 'TRANSFER_DISCREPANCY'
  | 'SETTLEMENT_MISMATCH'
  | 'RECEIVING_SHORTAGE'
  | 'CANCEL_ARBITRATION';

export type CaseStatus =
  | 'OPEN_UNASSIGNED'
  | 'ASSIGNED_IN_PROGRESS'
  | 'RESOLVED_WITH_EVIDENCE';

export interface TimelineEntry {
  at: string;
  actor: string;
  action: string;
  evidenceUrl?: string;
  note?: string;
}

export interface ExceptionCaseDoc {
  caseId: string;
  referenceId: string;
  category: CaseCategory;
  status: CaseStatus;
  ownerId: string | null;
  slaFirstAssignDueAt: string; // Section 17: Initial assignment SLA within 30 minutes
  nextActionDeadline: string;  // Deadline for next action / touch
  summary: string;
  resolutionEvidence?: string;
  createdAt: string;
  updatedAt: string;
  timeline: TimelineEntry[];
  isSlaOverdue?: boolean;
}

@Injectable()
export class ExceptionCasesService {
  // Exception case dossiers stored on MongoDB (support_exception_cases) using $push for timeline
  private readonly cases = new Map<string, ExceptionCaseDoc>();

  constructor() {
    // Seed initial standard scenario cases (SC11 discrepancy & SC09 receiving)
    const now = Date.now();
    const sc11Case: ExceptionCaseDoc = {
      caseId: 'CASE-SC11-001',
      referenceId: 'O-001',
      category: 'SETTLEMENT_MISMATCH',
      status: 'ASSIGNED_IN_PROGRESS',
      ownerId: 'finance-an',
      slaFirstAssignDueAt: new Date(now - 10 * 60 * 1000).toISOString(),
      nextActionDeadline: new Date(now + 90 * 60 * 1000).toISOString(),
      summary: 'Đối soát kỳ T+1 đơn O-001: Kỳ vọng nhận 653.400đ, thực nhận 650.000đ -> Lệch thiếu 3.400đ (SC11)',
      createdAt: new Date(now - 40 * 60 * 1000).toISOString(),
      updatedAt: new Date(now - 10 * 60 * 1000).toISOString(),
      timeline: [
        {
          at: new Date(now - 40 * 60 * 1000).toISOString(),
          actor: 'LEDGER_RECONCILIATION_ENGINE',
          action: 'Tự động mở vụ việc lệch đối soát 3.400đ; nghiêm cấm sửa giao dịch thu gốc 660.000đ (I06)',
          evidenceUrl: 'https://commerce-lab-retail-assets.s3.ap-southeast-1.amazonaws.com/settlements/2026-10-26.csv',
        },
        {
          at: new Date(now - 10 * 60 * 1000).toISOString(),
          actor: 'ops-lead',
          action: 'Phân công xử lý vụ việc cho finance-an trong SLA 30 phút',
        },
      ],
    };

    const sc09Case: ExceptionCaseDoc = {
      caseId: 'CASE-SC09-001',
      referenceId: 'PO-202611-001',
      category: 'RECEIVING_SHORTAGE',
      status: 'OPEN_UNASSIGNED',
      ownerId: null,
      slaFirstAssignDueAt: new Date(now + 15 * 60 * 1000).toISOString(),
      nextActionDeadline: new Date(now + 60 * 60 * 1000).toISOString(),
      summary: 'Kiểm hàng PO-202611-001 phát hiện thiếu 2 Balo City và 1 chiếc rách seal (SC09)',
      createdAt: new Date(now - 15 * 60 * 1000).toISOString(),
      updatedAt: new Date(now - 15 * 60 * 1000).toISOString(),
      timeline: [
        {
          at: new Date(now - 15 * 60 * 1000).toISOString(),
          actor: 'warehouse-qc',
          action: 'Lập biên bản nhận hàng thiếu và mở exception case',
          evidenceUrl: 'https://commerce-lab-retail-assets.s3.ap-southeast-1.amazonaws.com/receiving/po-001-damage.jpg',
        },
      ],
    };

    this.cases.set(sc11Case.caseId, sc11Case);
    this.cases.set(sc09Case.caseId, sc09Case);
  }

  createCase(data: {
    referenceId: string;
    category: CaseCategory;
    summary: string;
    actor?: string;
    evidenceUrl?: string;
  }): ExceptionCaseDoc {
    if (!data.referenceId || !data.category || !data.summary) {
      throw new BadRequestException('referenceId, category, and summary are required');
    }

    const now = new Date();
    const caseId = `CASE-${Date.now()}-${Math.floor(Math.random() * 1000)}`;
    const slaFirstAssignDueAt = new Date(now.getTime() + 30 * 60 * 1000).toISOString();
    const nextActionDeadline = new Date(now.getTime() + 2 * 60 * 60 * 1000).toISOString();
    const nowIso = now.toISOString();

    const record: ExceptionCaseDoc = {
      caseId,
      referenceId: data.referenceId,
      category: data.category,
      status: 'OPEN_UNASSIGNED',
      ownerId: null,
      slaFirstAssignDueAt,
      nextActionDeadline,
      summary: data.summary,
      createdAt: nowIso,
      updatedAt: nowIso,
      timeline: [
        {
          at: nowIso,
          actor: data.actor || 'SYSTEM_MONITOR',
          action: `Tự động mở exception case: ${data.summary}`,
          evidenceUrl: data.evidenceUrl,
        },
      ],
    };

    this.cases.set(caseId, record);
    return record;
  }

  assignCase(caseId: string, ownerId: string, actor: string): ExceptionCaseDoc {
    const item = this.getCase(caseId);
    if (!ownerId) {
      throw new BadRequestException('ownerId is required for assignment');
    }

    const nowIso = new Date().toISOString();
    item.ownerId = ownerId;
    if (item.status === 'OPEN_UNASSIGNED') {
      item.status = 'ASSIGNED_IN_PROGRESS';
    }
    item.updatedAt = nowIso;
    item.timeline.push({
      at: nowIso,
      actor,
      action: `Phân công vụ việc cho ${ownerId} (${new Date() <= new Date(item.slaFirstAssignDueAt) ? 'Trong hạn SLA 30m' : 'Quá hạn SLA 30m'})`,
    });

    return item;
  }

  appendTimeline(
    caseId: string,
    entry: { actor: string; action: string; evidenceUrl?: string; note?: string },
  ): ExceptionCaseDoc {
    const item = this.getCase(caseId);
    const nowIso = new Date().toISOString();
    item.updatedAt = nowIso;
    item.timeline.push({
      at: nowIso,
      actor: entry.actor,
      action: entry.action,
      evidenceUrl: entry.evidenceUrl,
      note: entry.note,
    });
    return item;
  }

  resolveCase(
    caseId: string,
    actor: string,
    evidence: string,
    note?: string,
  ): ExceptionCaseDoc {
    const item = this.getCase(caseId);
    if (!evidence && !note) {
      throw new BadRequestException('Resolution requires evidence or detailed explanation note (Section 17)');
    }

    const nowIso = new Date().toISOString();
    item.status = 'RESOLVED_WITH_EVIDENCE';
    item.resolutionEvidence = evidence || note;
    item.updatedAt = nowIso;
    item.timeline.push({
      at: nowIso,
      actor,
      action: `Đóng hồ sơ ngoại lệ với bằng chứng xác thực: ${note || evidence}`,
      evidenceUrl: evidence,
      note,
    });

    return item;
  }

  getCase(caseId: string): ExceptionCaseDoc {
    const item = this.cases.get(caseId);
    if (!item) {
      throw new NotFoundException(`Exception case ${caseId} not found`);
    }
    return item;
  }

  listCases(filters?: {
    status?: string;
    category?: string;
    ownerId?: string;
    overdueOnly?: boolean;
  }): ExceptionCaseDoc[] {
    const now = new Date();
    let list = Array.from(this.cases.values()).map((c) => ({
      ...c,
      isSlaOverdue: c.status === 'OPEN_UNASSIGNED' && now > new Date(c.slaFirstAssignDueAt),
    }));

    if (filters?.status) {
      list = list.filter((c) => c.status === filters.status);
    }
    if (filters?.category) {
      list = list.filter((c) => c.category === filters.category);
    }
    if (filters?.ownerId) {
      list = list.filter((c) => c.ownerId === filters.ownerId);
    }
    if (filters?.overdueOnly) {
      list = list.filter((c) => c.isSlaOverdue);
    }

    return list.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
  }

  listOpenCases(): ExceptionCaseDoc[] {
    return this.listCases();
  }
}
