import { Injectable } from '@nestjs/common';

export interface ExceptionCaseDoc {
  caseId: string;
  referenceId: string;
  category: 'PAYMENT_UNKNOWN_EXPIRED' | 'PICK_SHORTFALL' | 'TRANSFER_DISCREPANCY' | 'SETTLEMENT_MISMATCH';
  status: 'OPEN_UNASSIGNED' | 'ASSIGNED_IN_PROGRESS' | 'RESOLVED_WITH_EVIDENCE';
  ownerId: string | null;
  slaFirstAssignDueAt: string; // Mục 17: Phân công lần đầu trong 30 phút
  summary: string;
  timeline: Array<{ at: string; actor: string; action: string; evidenceUrl?: string }>;
}

@Injectable()
export class ExceptionCasesService {
  // Hồ sơ vụ việc ngoại lệ lưu trên MongoDB (support_exception_cases) dùng $push timeline
  private readonly cases: ExceptionCaseDoc[] = [
    {
      caseId: 'CASE-SC11-001',
      referenceId: 'O-001',
      category: 'SETTLEMENT_MISMATCH',
      status: 'ASSIGNED_IN_PROGRESS',
      ownerId: 'finance-an',
      slaFirstAssignDueAt: new Date(Date.now() + 30 * 60 * 1000).toISOString(),
      summary: 'Đối soát kỳ T+1 đơn O-001: Kỳ vọng nhận 653.400đ, thực nhận 650.000đ -> Lệch thiếu 3.400đ (SC11)',
      timeline: [
        {
          at: new Date().toISOString(),
          actor: 'LEDGER_RECONCILIATION_ENGINE',
          action: 'Tự động mở vụ việc lệch đối soát 3.400đ; nghiêm cấm sửa giao dịch thu gốc 660.000đ (I06)',
          evidenceUrl: 'https://commerce-lab-retail-assets.s3.ap-southeast-1.amazonaws.com/settlements/2026-10-26.csv',
        },
      ],
    },
  ];

  listOpenCases(): ExceptionCaseDoc[] {
    return this.cases;
  }
}
