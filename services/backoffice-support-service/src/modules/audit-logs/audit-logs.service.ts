import { Injectable } from '@nestjs/common';
import { DatabaseService } from '../../database/database.service';
import { redactObject } from '../../common/privacy/pii-redactor';

export interface AuditLogEntry {
  id: string;
  source: 'IAM' | 'APPROVAL' | 'EXCEPTION_CASE' | 'RETURNS';
  event: string;
  actorUserId: string;
  targetId?: string;
  outcome: 'SUCCESS' | 'FAILURE' | 'BLOCKED';
  reason?: string;
  detail?: Record<string, any>;
  at: string;
}

@Injectable()
export class AuditLogsService {
  private readonly inMemoryLogs: AuditLogEntry[] = [
    {
      id: 'AUD-001',
      source: 'IAM',
      event: 'LOGIN_SUCCESS',
      actorUserId: 'admin-user',
      targetId: 'admin-user',
      outcome: 'SUCCESS',
      detail: { ip: '127.0.0.1', email: 'a***@example.com' },
      at: new Date(Date.now() - 3600 * 1000).toISOString(),
    },
    {
      id: 'AUD-002',
      source: 'APPROVAL',
      event: 'APPROVAL_DECIDED',
      actorUserId: 'lead-binh',
      targetId: 'APP-001',
      outcome: 'SUCCESS',
      reason: 'Approved after verifying damage photo',
      detail: { requestType: 'EXCEPTION_REFUND', amountVnd: 270000 },
      at: new Date(Date.now() - 1800 * 1000).toISOString(),
    },
    {
      id: 'AUD-003',
      source: 'EXCEPTION_CASE',
      event: 'CASE_ASSIGNED',
      actorUserId: 'ops-lead',
      targetId: 'CASE-SC11-001',
      outcome: 'SUCCESS',
      reason: 'Assigned within 30-min SLA to finance officer',
      detail: { ownerId: 'finance-an' },
      at: new Date(Date.now() - 600 * 1000).toISOString(),
    },
  ];

  constructor(private readonly db?: DatabaseService) {}

  record(entry: Omit<AuditLogEntry, 'id' | 'at'> & { at?: string }): AuditLogEntry {
    const id = `AUD-${Date.now()}-${Math.floor(Math.random() * 1000)}`;
    const log: AuditLogEntry = {
      id,
      at: entry.at || new Date().toISOString(),
      ...entry,
      detail: this.maskPii(entry.detail),
    };
    this.inMemoryLogs.unshift(log);
    return log;
  }

  async listAuditLogs(filters?: {
    actorId?: string;
    event?: string;
    source?: string;
    from?: string;
    to?: string;
  }): Promise<AuditLogEntry[]> {
    if (this.db) {
      try {
        const query = `
          SELECT id::text, 'IAM' as source, event, COALESCE(actor_user_id, '') as actor_user_id,
                 COALESCE(target_user_id, '') as target_id, outcome, COALESCE(reason, '') as reason,
                 detail, at
          FROM iam.auth_audit_logs
          ORDER BY id DESC
          LIMIT 50
        `;
        const res = await this.db.query(query);
        if (res.rows && res.rows.length > 0) {
          const dbLogs: AuditLogEntry[] = res.rows.map((r: any) => ({
            id: `IAM-${r.id}`,
            source: 'IAM',
            event: r.event,
            actorUserId: r.actor_user_id,
            targetId: r.target_id,
            outcome: (r.outcome || 'SUCCESS').toUpperCase(),
            reason: r.reason,
            detail: this.maskPii(r.detail),
            at: new Date(r.at).toISOString(),
          }));
          return this.applyFilters([...this.inMemoryLogs, ...dbLogs], filters);
        }
      } catch {
        // Fallback in-memory
      }
    }

    return this.applyFilters(this.inMemoryLogs, filters);
  }

  private applyFilters(
    logs: AuditLogEntry[],
    filters?: {
      actorId?: string;
      event?: string;
      source?: string;
      from?: string;
      to?: string;
    },
  ): AuditLogEntry[] {
    let result = [...logs];
    if (filters?.actorId) {
      result = result.filter((l) => l.actorUserId === filters.actorId);
    }
    if (filters?.event) {
      result = result.filter((l) => l.event === filters.event);
    }
    if (filters?.source) {
      result = result.filter((l) => l.source === filters.source);
    }
    if (filters?.from) {
      const fromTime = new Date(filters.from).getTime();
      result = result.filter((l) => new Date(l.at).getTime() >= fromTime);
    }
    if (filters?.to) {
      const toTime = new Date(filters.to).getTime();
      result = result.filter((l) => new Date(l.at).getTime() <= toTime);
    }
    return result.sort((a, b) => new Date(b.at).getTime() - new Date(a.at).getTime());
  }

  maskPii(detail?: Record<string, any>): Record<string, any> | undefined {
    return redactObject(detail);
  }
}
