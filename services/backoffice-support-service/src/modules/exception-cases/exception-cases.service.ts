import { BadRequestException, Injectable, NotFoundException, Optional } from '@nestjs/common';
import { DatabaseService } from '../../database/database.service';

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
  // In-memory store used only when DatabaseService is omitted (unit tests).
  private readonly cases = new Map<string, ExceptionCaseDoc>();
  private seededDb = false;

  constructor(@Optional() private readonly db?: DatabaseService) {
    if (!this.db) {
      this.seedInMemory();
    }
  }

  private seedInMemory(): void {
    const now = Date.now();
    const sc11Case: ExceptionCaseDoc = {
      caseId: 'CASE-SC11-001',
      referenceId: 'O-001',
      category: 'SETTLEMENT_MISMATCH',
      status: 'ASSIGNED_IN_PROGRESS',
      ownerId: 'finance-an',
      slaFirstAssignDueAt: new Date(now - 10 * 60 * 1000).toISOString(),
      nextActionDeadline: new Date(now + 90 * 60 * 1000).toISOString(),
      summary: 'Period T+1 reconciliation for O-001: expected 653,400 VND, received 650,000 VND -> 3,400 VND shortfall (SC11)',
      createdAt: new Date(now - 40 * 60 * 1000).toISOString(),
      updatedAt: new Date(now - 10 * 60 * 1000).toISOString(),
      timeline: [
        {
          at: new Date(now - 40 * 60 * 1000).toISOString(),
          actor: 'LEDGER_RECONCILIATION_ENGINE',
          action: 'Auto-opened 3,400 VND discrepancy case; original 660,000 VND collection preserved (I06)',
          evidenceUrl: 'https://commerce-lab-retail-assets.s3.ap-southeast-1.amazonaws.com/settlements/2026-10-26.csv',
        },
        {
          at: new Date(now - 10 * 60 * 1000).toISOString(),
          actor: 'ops-lead',
          action: 'Assigned to finance-an within 30-minute SLA',
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
      summary: 'Receiving inspection on PO-202611-001 found 2 missing City Backpacks and 1 damaged seal (SC09)',
      createdAt: new Date(now - 15 * 60 * 1000).toISOString(),
      updatedAt: new Date(now - 15 * 60 * 1000).toISOString(),
      timeline: [
        {
          at: new Date(now - 15 * 60 * 1000).toISOString(),
          actor: 'warehouse-qc',
          action: 'Recorded receiving shortage and opened exception case',
          evidenceUrl: 'https://commerce-lab-retail-assets.s3.ap-southeast-1.amazonaws.com/receiving/po-001-damage.jpg',
        },
      ],
    };

    this.cases.set(sc11Case.caseId, sc11Case);
    this.cases.set(sc09Case.caseId, sc09Case);
  }

  private mapRow(row: any): ExceptionCaseDoc {
    const timeline: TimelineEntry[] = Array.isArray(row.timeline_json)
      ? row.timeline_json
      : typeof row.timeline_json === 'string'
      ? JSON.parse(row.timeline_json)
      : [];
    const slaDue = new Date(row.sla_first_assign_due_at);
    return {
      caseId: row.case_id,
      referenceId: row.reference_id,
      category: row.category,
      status: row.status,
      ownerId: row.owner_id ?? null,
      slaFirstAssignDueAt: slaDue.toISOString(),
      nextActionDeadline: new Date(row.next_action_deadline).toISOString(),
      summary: row.summary,
      resolutionEvidence: row.resolution_evidence ?? undefined,
      createdAt: new Date(row.created_at).toISOString(),
      updatedAt: new Date(row.updated_at).toISOString(),
      timeline,
      isSlaOverdue: row.status === 'OPEN_UNASSIGNED' && Date.now() > slaDue.getTime(),
    };
  }

  private async ensureSeededInDb(): Promise<void> {
    if (!this.db || this.seededDb) return;
    this.seededDb = true;
    const countRes = await this.db.query<{ count: string }>('SELECT COUNT(*) AS count FROM support.exception_cases');
    if (Number(countRes.rows[0]?.count || 0) > 0) return;

    this.seedInMemory();
    for (const c of this.cases.values()) {
      await this.db.query(
        `INSERT INTO support.exception_cases (
          case_id, reference_id, category, status, owner_id,
          sla_first_assign_due_at, next_action_deadline, summary,
          resolution_evidence, timeline_json, created_at, updated_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)
        ON CONFLICT (case_id) DO NOTHING`,
        [
          c.caseId,
          c.referenceId,
          c.category,
          c.status,
          c.ownerId,
          c.slaFirstAssignDueAt,
          c.nextActionDeadline,
          c.summary,
          c.resolutionEvidence ?? null,
          JSON.stringify(c.timeline),
          c.createdAt,
          c.updatedAt,
        ],
      );
    }
  }

  async createCase(data: {
    referenceId: string;
    category: CaseCategory;
    summary: string;
    actor?: string;
    evidenceUrl?: string;
  }): Promise<ExceptionCaseDoc> {
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
          action: `Opened exception case: ${data.summary}`,
          evidenceUrl: data.evidenceUrl,
        },
      ],
    };

    if (!this.db) {
      this.cases.set(caseId, record);
      return record;
    }

    await this.ensureSeededInDb();
    await this.db.query(
      `INSERT INTO support.exception_cases (
        case_id, reference_id, category, status, owner_id,
        sla_first_assign_due_at, next_action_deadline, summary,
        timeline_json, created_at, updated_at
      ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11)`,
      [
        record.caseId,
        record.referenceId,
        record.category,
        record.status,
        record.ownerId,
        record.slaFirstAssignDueAt,
        record.nextActionDeadline,
        record.summary,
        JSON.stringify(record.timeline),
        record.createdAt,
        record.updatedAt,
      ],
    );
    return { ...record, isSlaOverdue: false };
  }

  async assignCase(caseId: string, ownerId: string, actor: string): Promise<ExceptionCaseDoc> {
    const item = await this.getCase(caseId);
    if (!ownerId) {
      throw new BadRequestException('ownerId is required for assignment');
    }

    const nowIso = new Date().toISOString();
    const withinSla = new Date() <= new Date(item.slaFirstAssignDueAt);
    item.ownerId = ownerId;
    if (item.status === 'OPEN_UNASSIGNED') {
      item.status = 'ASSIGNED_IN_PROGRESS';
    }
    item.updatedAt = nowIso;
    item.timeline.push({
      at: nowIso,
      actor,
      action: `Assigned case to ${ownerId} (${withinSla ? 'Within 30m SLA' : 'Overdue 30m SLA'})`,
    });

    if (!this.db) {
      return item;
    }

    await this.db.query(
      `UPDATE support.exception_cases
       SET owner_id = $1, status = $2, timeline_json = $3::jsonb, updated_at = $4
       WHERE case_id = $5`,
      [item.ownerId, item.status, JSON.stringify(item.timeline), item.updatedAt, caseId],
    );
    item.isSlaOverdue = false;
    return item;
  }

  async appendTimeline(
    caseId: string,
    entry: { actor: string; action: string; evidenceUrl?: string; note?: string },
  ): Promise<ExceptionCaseDoc> {
    const item = await this.getCase(caseId);
    const nowIso = new Date().toISOString();
    item.updatedAt = nowIso;
    item.timeline.push({
      at: nowIso,
      actor: entry.actor,
      action: entry.action,
      evidenceUrl: entry.evidenceUrl,
      note: entry.note,
    });

    if (this.db) {
      await this.db.query(
        `UPDATE support.exception_cases
         SET timeline_json = $1::jsonb, updated_at = $2
         WHERE case_id = $3`,
        [JSON.stringify(item.timeline), item.updatedAt, caseId],
      );
    }
    return item;
  }

  async resolveCase(
    caseId: string,
    actor: string,
    evidence: string,
    note?: string,
  ): Promise<ExceptionCaseDoc> {
    const item = await this.getCase(caseId);
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
      action: `Resolved exception case with evidence: ${note || evidence}`,
      evidenceUrl: evidence,
      note,
    });

    if (this.db) {
      await this.db.query(
        `UPDATE support.exception_cases
         SET status = $1, resolution_evidence = $2, timeline_json = $3::jsonb, updated_at = $4
         WHERE case_id = $5`,
        [item.status, item.resolutionEvidence, JSON.stringify(item.timeline), item.updatedAt, caseId],
      );
    }
    item.isSlaOverdue = false;
    return item;
  }

  async getCase(caseId: string): Promise<ExceptionCaseDoc> {
    if (!this.db) {
      const item = this.cases.get(caseId);
      if (!item) {
        throw new NotFoundException(`Exception case ${caseId} not found`);
      }
      return item;
    }

    await this.ensureSeededInDb();
    const res = await this.db.query(
      'SELECT * FROM support.exception_cases WHERE case_id = $1',
      [caseId],
    );
    if (res.rows.length === 0) {
      throw new NotFoundException(`Exception case ${caseId} not found`);
    }
    return this.mapRow(res.rows[0]);
  }

  async listCases(filters?: {
    status?: string;
    category?: string;
    ownerId?: string;
    overdueOnly?: boolean;
  }): Promise<ExceptionCaseDoc[]> {
    const now = new Date();
    let list: ExceptionCaseDoc[];

    if (!this.db) {
      list = Array.from(this.cases.values()).map((c) => ({
        ...c,
        isSlaOverdue: c.status === 'OPEN_UNASSIGNED' && now > new Date(c.slaFirstAssignDueAt),
      }));
    } else {
      await this.ensureSeededInDb();
      const res = await this.db.query(
        'SELECT * FROM support.exception_cases ORDER BY created_at DESC',
      );
      list = res.rows.map((r) => this.mapRow(r));
    }

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

  async listOpenCases(): Promise<ExceptionCaseDoc[]> {
    return this.listCases();
  }
}
