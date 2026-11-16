import { Body, Controller, Get, Post, UseGuards } from '@nestjs/common';
import { ApprovalCandidate, MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';
import { CreateReturnDto } from './modules/returns-rma/create-return.dto';
import { ApiResult } from './common/http/api-response.interceptor';
import { DatabaseService } from './database/database.service';
import { JwtAuthGuard } from './common/guards/jwt-auth.guard';
import { RolesGuard } from './common/guards/roles.guard';
import { Roles } from './common/guards/roles.decorator';

@Controller()
export class AppController {
  constructor(
    private readonly makerChecker: MakerCheckerPolicyService,
    private readonly returnsService: ReturnsRmaService,
    private readonly exceptionCases: ExceptionCasesService,
    private readonly databaseService: DatabaseService,
  ) {}

  @Get('/health')
  async health() {
    const dbOk = await this.databaseService.ping();
    return new ApiResult(
      {
        service: 'backoffice-support-service',
        runtime: 'NestJS',
        postgresSchema: 'support',
        postgresConnected: dbOk,
      },
      'backoffice-support-service healthy',
    );
  }

  @Get('/api/v1/backoffice/exception-cases')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
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
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
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
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'ADMIN')
  validateApproval(@Body() body: ApprovalCandidate) {
    this.makerChecker.validateApproval(body);
    return new ApiResult(
      {
        approved: true,
        ...body,
      },
      'Approval is valid',
    );
  }
}
