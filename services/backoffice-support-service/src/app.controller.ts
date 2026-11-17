import {
  Body,
  Controller,
  Get,
  NotFoundException,
  Param,
  Post,
  Query,
  Req,
  UseGuards,
} from '@nestjs/common';
import { ApprovalCandidate, ApprovalType, MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';
import { AuditLogsService } from './modules/audit-logs/audit-logs.service';
import { ManagementMetricsService } from './modules/metrics/management-metrics.service';
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
    private readonly auditLogs: AuditLogsService,
    private readonly metricsService: ManagementMetricsService,
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
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'FINANCE', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
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
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'FINANCE', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  getBackofficeReturnDetail(@Param('rmaId') rmaId: string) {
    const item = this.returnsService.getReturn(rmaId);
    return new ApiResult(item, 'Backoffice return detail retrieved successfully');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/approve')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'OPS_MGR', 'ADMIN')
  approveBackofficeReturn(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'support-agent';
    const item = this.returnsService.approveReturn(rmaId, actor);
    return new ApiResult(item, 'Return request approved successfully');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/receive')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'OPS_MGR', 'ADMIN')
  receiveReturnParcel(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'warehouse-staff';
    const item = this.returnsService.receiveRma(rmaId, actor);
    return new ApiResult(item, 'Return parcel received by warehouse (SC07)');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/qc')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'OPS_MGR', 'ADMIN')
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
  @Roles('SUPPORT_LEAD', 'FINANCE', 'FINANCE_STAFF', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  async disburseReturnRefund(@Req() req: any, @Param('rmaId') rmaId: string) {
    const actor = req.user?.userId || 'finance-officer';
    const item = await this.returnsService.disburseRefund(rmaId, actor);
    return new ApiResult(item, 'Refund disbursed successfully through payment simulator (I03/SC08)');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/request-approval')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'OPS_MGR', 'ADMIN')
  async requestRefundApproval(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('reason') reason?: string,
  ) {
    const makerUserId = req.user?.userId || 'support-agent';
    const record = await this.returnsService.requestRefundApproval(rmaId, makerUserId, reason);
    return new ApiResult(record, 'Refund approval request created successfully');
  }

  @Post('/api/v1/backoffice/approvals')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'PROCUREMENT', 'PROCUREMENT_STAFF', 'PROCUREMENT_MGR', 'OPS_MGR', 'ADMIN')
  async createApproval(
    @Req() req: any,
    @Body() body: {
      requestType: ApprovalType;
      referenceId: string;
      amountVnd: number;
      quantityDiff?: number;
      reason: string;
    },
  ) {
    const makerUserId = req.user?.userId || 'staff-maker';
    const record = await this.makerChecker.createApprovalRequest({
      ...body,
      makerUserId,
    });
    return new ApiResult(record, 'Approval request created successfully');
  }

  @Get('/api/v1/backoffice/approvals')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE', 'FINANCE_MGR', 'PROCUREMENT', 'PROCUREMENT_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'ADMIN')
  async listApprovals(
    @Query('status') status?: string,
    @Query('requestType') requestType?: any,
    @Query('makerUserId') makerUserId?: string,
  ) {
    const list = await this.makerChecker.listApprovals({ status, requestType, makerUserId });
    return new ApiResult(list, 'Approvals retrieved successfully');
  }

  @Get('/api/v1/backoffice/approvals/:id')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE', 'FINANCE_MGR', 'PROCUREMENT', 'PROCUREMENT_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'ADMIN')
  async getApprovalDetail(@Param('id') id: string) {
    const item = await this.makerChecker.getApproval(id);
    if (!item) {
      throw new NotFoundException(`Approval request ${id} not found`);
    }
    return new ApiResult(item, 'Approval detail retrieved successfully');
  }

  @Post('/api/v1/backoffice/approvals/:id/decide')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'PROCUREMENT_MGR', 'OPS_MGR', 'ADMIN')
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
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'OPS_MGR', 'ADMIN')
  rejectBackofficeReturn(
    @Req() req: any,
    @Param('rmaId') rmaId: string,
    @Body('note') note: string,
  ) {
    const actor = req.user?.userId || 'support-agent';
    const item = this.returnsService.rejectReturn(rmaId, note || 'Ineligible return request', actor);
    return new ApiResult(item, 'Return request rejected and ceiling released');
  }

  @Post('/api/v1/backoffice/returns/:rmaId/exchange')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'OPS_MGR', 'ADMIN')
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

  @Post('/api/v1/backoffice/exception-cases')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'DELIVERY_COORD', 'FINANCE', 'FINANCE_STAFF', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  async createExceptionCase(
    @Req() req: any,
    @Body() body: {
      referenceId: string;
      category: any;
      summary: string;
      evidenceUrl?: string;
    },
  ) {
    const actor = req.user?.userId || 'system-staff';
    const item = await this.exceptionCases.createCase({
      ...body,
      actor,
    });
    return new ApiResult(item, 'Exception case created successfully');
  }

  @Get('/api/v1/backoffice/exception-cases')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'DELIVERY_COORD', 'FINANCE', 'FINANCE_STAFF', 'FINANCE_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'ADMIN')
  async listExceptionCases(
    @Query('status') status?: string,
    @Query('category') category?: string,
    @Query('ownerId') ownerId?: string,
    @Query('overdueOnly') overdueOnly?: string,
  ) {
    const items = await this.exceptionCases.listCases({
      status,
      category,
      ownerId,
      overdueOnly: overdueOnly === 'true',
    });
    return new ApiResult(items, 'Exception cases retrieved successfully', {
      total: items.length,
      page: 1,
      pageCount: 1,
      limit: items.length || 20,
    });
  }

  @Get('/api/v1/backoffice/exception-cases/:caseId')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'DELIVERY_COORD', 'FINANCE', 'FINANCE_STAFF', 'FINANCE_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'ADMIN')
  async getExceptionCaseDetail(@Param('caseId') caseId: string) {
    const item = await this.exceptionCases.getCase(caseId);
    return new ApiResult(item, 'Exception case retrieved successfully');
  }

  @Post('/api/v1/backoffice/exception-cases/:caseId/assign')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  async assignExceptionCase(
    @Req() req: any,
    @Param('caseId') caseId: string,
    @Body('ownerId') ownerId: string,
  ) {
    const actor = req.user?.userId || 'ops-lead';
    const item = await this.exceptionCases.assignCase(caseId, ownerId, actor);
    return new ApiResult(item, `Exception case assigned to ${ownerId} successfully`);
  }

  @Post('/api/v1/backoffice/exception-cases/:caseId/timeline')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('CSKH', 'SUPPORT_AGENT', 'SUPPORT_STAFF', 'SUPPORT_LEAD', 'WAREHOUSE_STAFF', 'WAREHOUSE_MGR', 'DELIVERY_COORD', 'FINANCE', 'FINANCE_STAFF', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  async appendCaseTimeline(
    @Req() req: any,
    @Param('caseId') caseId: string,
    @Body() body: { action: string; evidenceUrl?: string; note?: string },
  ) {
    const actor = req.user?.userId || 'staff';
    const item = await this.exceptionCases.appendTimeline(caseId, {
      ...body,
      actor,
    });
    return new ApiResult(item, 'Timeline entry added to exception case');
  }

  @Post('/api/v1/backoffice/exception-cases/:caseId/resolve')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'OPS_MGR', 'ADMIN')
  async resolveExceptionCase(
    @Req() req: any,
    @Param('caseId') caseId: string,
    @Body('evidence') evidence: string,
    @Body('note') note?: string,
  ) {
    const actor = req.user?.userId || 'ops-lead';
    const item = await this.exceptionCases.resolveCase(caseId, actor, evidence, note);
    return new ApiResult(item, 'Exception case resolved with evidence successfully');
  }

  // ==========================================
  // AUDIT LOGS QUERY API (P11-T04)
  // ==========================================

  @Get('/api/v1/backoffice/audit-logs')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'ADMIN')
  async listAuditLogs(
    @Query('actorId') actorId?: string,
    @Query('event') event?: string,
    @Query('source') source?: string,
    @Query('from') from?: string,
    @Query('to') to?: string,
  ) {
    const items = await this.auditLogs.listAuditLogs({
      actorId,
      event,
      source,
      from,
      to,
    });
    return new ApiResult(items, 'Audit logs retrieved successfully', {
      total: items.length,
      page: 1,
      pageCount: 1,
      limit: items.length || 50,
    });
  }

  // ==========================================
  // MANAGEMENT METRICS API (P11-T05, SC12)
  // ==========================================

  @Get('/api/v1/backoffice/metrics')
  @UseGuards(JwtAuthGuard, RolesGuard)
  @Roles('SUPPORT_LEAD', 'WAREHOUSE_MGR', 'FINANCE', 'FINANCE_MGR', 'OPS_MGR', 'ACCESS_ADMIN', 'PRODUCT_STAFF', 'MARKETING', 'PROCUREMENT', 'DELIVERY_COORD', 'WAREHOUSE_STAFF', 'ADMIN')
  async getManagementMetrics(@Query('period') period?: string) {
    const data = await this.metricsService.getMetrics(period || 'TODAY');
    return new ApiResult(data, 'Management metrics retrieved successfully');
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
