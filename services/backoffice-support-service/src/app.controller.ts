import {
  Body,
  Controller,
  Get,
  Param,
  Post,
  Query,
  Req,
  UseGuards,
} from '@nestjs/common';
import { ApprovalCandidate, MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';
import { CreateReturnRequestDto } from './modules/returns-rma/create-return.dto';
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

  // ==========================================
  // CUSTOMER & GENERAL RETURNS API (/api/v1/returns)
  // ==========================================

  @Post('/api/v1/returns')
  @UseGuards(JwtAuthGuard)
  async createReturn(@Req() req: any, @Body() body: CreateReturnRequestDto) {
    const customerId = req.user?.userId || 'cust-default';
    const result = await this.returnsService.openRmaRequest(body, customerId);
    return new ApiResult(result, 'Return request created successfully');
  }

  @Get('/api/v1/returns')
  @UseGuards(JwtAuthGuard)
  listReturns(
    @Req() req: any,
    @Query('orderId') orderId?: string,
    @Query('status') status?: string,
  ) {
    const customerId = req.user?.roles?.includes('ADMIN') ? undefined : req.user?.userId;
    const items = this.returnsService.listReturns({ orderId, status, customerId });
    return new ApiResult(items, 'Returns retrieved successfully', {
      total: items.length,
      page: 1,
      pageCount: 1,
      limit: items.length || 20,
    });
  }

  @Get('/api/v1/returns/:returnId')
  @UseGuards(JwtAuthGuard)
  getReturnDetail(@Param('returnId') returnId: string) {
    const item = this.returnsService.getReturn(returnId);
    return new ApiResult(item, 'Return detail retrieved successfully');
  }

  @Post('/api/v1/orders/:orderId/returns')
  @UseGuards(JwtAuthGuard)
  async createOrderReturn(
    @Req() req: any,
    @Param('orderId') orderId: string,
    @Body() body: CreateReturnRequestDto,
  ) {
    body.orderId = orderId;
    const customerId = req.user?.userId || 'cust-default';
    const result = await this.returnsService.openRmaRequest(body, customerId);
    return new ApiResult(result, 'Return request created successfully');
  }

  @Get('/api/v1/orders/:orderId/returns')
  @UseGuards(JwtAuthGuard)
  listOrderReturns(@Param('orderId') orderId: string) {
    const items = this.returnsService.listReturns({ orderId });
    return new ApiResult(items, 'Order returns retrieved successfully');
  }

  // ==========================================
  // BACKOFFICE & STAFF RETURNS API (/api/v1/backoffice/returns)
  // ==========================================

  @Get('/api/v1/backoffice/returns')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'ADMIN')
  listBackofficeReturns(
    @Query('status') status?: string,
    @Query('orderId') orderId?: string,
    @Query('warehouseId') warehouseId?: string,
  ) {
    const items = this.returnsService.listReturns({ status, orderId, warehouseId });
    return new ApiResult(items, 'Backoffice returns retrieved successfully', {
      total: items.length,
      page: 1,
      pageCount: 1,
      limit: 20,
    });
  }

  @Get('/api/v1/backoffice/returns/:rmaId')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'ADMIN')
  getBackofficeReturnDetail(@Param('rmaId') rmaId: string) {
    const item = this.returnsService.getReturn(rmaId);
    return new ApiResult(item, 'Backoffice return detail retrieved successfully');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/approve')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
  approveBackofficeReturn(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'support-agent';
    const item = this.returnsService.approveReturn(rmaId, actor);
    return new ApiResult(item, 'Return request approved successfully');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/receive')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'ADMIN')
  receiveReturnParcel(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'warehouse-staff';
    const item = this.returnsService.receiveRma(rmaId, actor);
    return new ApiResult(item, 'Return parcel received by warehouse (SC07)');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/qc')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'ADMIN')
  submitQcInspection(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('results') results: Array<{ skuId: string; passedQuantity: number; failedQuantity: number }>,
  ) {
    const actor = req.user?.userId || 'qc-inspector';
    const item = this.returnsService.submitQcResult(rmaId, results || [], actor);
    return new ApiResult(item, 'QC inspection recorded successfully (SC07)');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/refund')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'FINANCE_STAFF', 'FINANCE_MGR', 'ADMIN')
  async disburseReturnRefund(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'finance-officer';
    const item = await this.returnsService.disburseRefund(rmaId, actor);
    return new ApiResult(item, 'Refund disbursed successfully through payment simulator (I03/SC08)');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/request-approval')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
  async requestRefundApproval(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('reason') reason?: string,
  ) {
    const makerUserId = req.user?.userId || 'support-agent';
    const record = await this.returnsService.requestRefundApproval(rmaId, makerUserId, reason);
    return new ApiResult(record, 'Refund approval request created successfully');
  }

  @Get('/api/v1/backoffice/approvals')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'ADMIN')
  listApprovals(
    @Query('status') status?: string,
    @Query('requestType') requestType?: any,
  ) {
    const list = this.makerChecker.listApprovals({ status, requestType });
    return new ApiResult(list, 'Approvals retrieved successfully');
  }

  @Post('/api/v1/backoffice/approvals/:id/decide')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'ADMIN')
  async decideApproval(
    @Req() req: any,
    @Param('id') id: string,
    @Body('action') action: 'APPROVE' | 'REJECT',
    @Body('note') note?: string,
  ) {
    const checkerUserId = req.user?.userId || 'approver';
    const checkerRole = req.user?.roles?.[0] || 'ADMIN';
    const result = await this.makerChecker.decideApproval(
      id,
      checkerUserId,
      checkerRole,
      action,
      note,
    );
    return new ApiResult(result, `Approval ${action.toLowerCase()}d successfully`);
  }

  @Post('/api/v1/backoffice/returns/:rmaId/reject')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
  rejectBackofficeReturn(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('note') note: string,
  ) {
    const actor = req.user?.userId || 'support-agent';
    const item = this.returnsService.rejectReturn(rmaId, note || 'Không đủ điều kiện đổi trả', actor);
    return new ApiResult(item, 'Return request rejected and ceiling released');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/exchange')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_STAFF', 'SUPPORT_LEAD', 'ADMIN')
  async createExchangeOrder(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('replacementSkuId') replacementSkuId?: string,
  ) {
    const actor = req.user?.userId || 'support-agent';
    const item = await this.returnsService.createLinkedExchangeOrder(
      rmaId,
      replacementSkuId,
      actor,
    );
    return new ApiResult(item, 'Exchange order linked successfully (B06)');
  }

  // ==========================================
  // EXCEPTION CASES & APPROVALS
  // ==========================================

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
