import { Body, Controller, Get, Post } from '@nestjs/common';
import { ApprovalCandidate, MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';

@Controller()
export class AppController {
  constructor(
    private readonly makerChecker: MakerCheckerPolicyService,
    private readonly returnsService: ReturnsRmaService,
    private readonly exceptionCases: ExceptionCasesService,
  ) {}

  @Get('/health')
  health() {
    return {
      message: 'backoffice-support-service healthy',
      data: {
        service: 'backoffice-support-service',
        runtime: 'NestJS',
        postgresSchema: 'support',
        mongoCollections: ['support_exception_cases', 'support_rma_dossiers'],
      },
    };
  }

  @Get('/api/v1/backoffice/exception-cases')
  listExceptionCases() {
    const items = this.exceptionCases.listOpenCases();
    return {
      message: 'Lấy danh sách Vụ việc ngoại lệ thành công (Mục 17)',
      data: items,
      metadata: {
        total: items.length,
        page: 1,
        pageCount: 1,
        limit: 20,
      },
    };
  }

  @Post('/api/v1/backoffice/returns')
  createReturnRequest(
    @Body()
    body: {
      rmaId?: string;
      orderId: string;
      skuId: string;
      quantity: number;
      reason: 'CHANGE_OF_MIND' | 'SELLER_FAULT';
    },
  ) {
    const dossier = this.returnsService.openRmaRequest(
      body.rmaId || `RMA-${Date.now()}`,
      body.orderId,
      body.skuId,
      body.quantity,
      body.reason,
    );
    return {
      message: 'Tạo hồ sơ trả hàng RMA và khóa trần số lượng I07 thành công',
      data: dossier,
    };
  }

  @Post('/api/v1/backoffice/approvals/validate')
  validateApproval(@Body() body: ApprovalCandidate) {
    this.makerChecker.validateApproval(body);
    return {
      message: 'Phê duyệt hợp lệ theo Policy P10 (Maker != Checker) và đúng hạn mức thẩm quyền',
      data: {
        approved: true,
        ...body,
      },
    };
  }
}
