import { Body, Controller, Get, Post } from '@nestjs/common';
import { ApprovalCandidate, MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';
import { CreateReturnDto } from './modules/returns-rma/create-return.dto';
import { ApiResult } from './common/http/api-response.interceptor';

@Controller()
export class AppController {
  constructor(
    private readonly makerChecker: MakerCheckerPolicyService,
    private readonly returnsService: ReturnsRmaService,
    private readonly exceptionCases: ExceptionCasesService,
  ) {}

  @Get('/health')
  health() {
    return new ApiResult({
      service: 'backoffice-support-service',
      runtime: 'NestJS',
      postgresSchema: 'support',
      mongoCollections: ['support_exception_cases', 'support_rma_dossiers'],
    }, 'backoffice-support-service healthy');
  }

  @Get('/api/v1/backoffice/exception-cases')
  listExceptionCases() {
    const items = this.exceptionCases.listOpenCases();
    return new ApiResult(items, 'Exception cases retrieved successfully', {
      total: items.length,
      page: 1,
      pageCount: 1,
      limit: 20,
    });
  }

  @Post('/api/v1/backoffice/returns')
  createReturnRequest(
    @Body()
    body: CreateReturnDto,
  ) {
    const dossier = this.returnsService.openRmaRequest(
      body.rmaId || `RMA-${Date.now()}`,
      body.orderId,
      body.skuId,
      body.quantity,
      body.reason,
    );
    return new ApiResult(dossier, 'Return request created successfully');
  }

  @Post('/api/v1/backoffice/approvals/validate')
  validateApproval(@Body() body: ApprovalCandidate) {
    this.makerChecker.validateApproval(body);
    return new ApiResult({
      approved: true,
      ...body,
    }, 'Approval is valid');
  }
}
