import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { JwtAuthGuard } from './common/guards/jwt-auth.guard';
import { RolesGuard } from './common/guards/roles.guard';
import { WarehouseScopeGuard } from './common/guards/warehouse.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';
import { DatabaseModule } from './database/database.module';
import { PaymentClient } from './modules/returns-rma/payment.client';

@Module({
  imports: [DatabaseModule],
  controllers: [AppController],
  providers: [
    MakerCheckerPolicyService,
    JwtAuthGuard,
    RolesGuard,
    WarehouseScopeGuard,
    ReturnsRmaService,
    ExceptionCasesService,
    PaymentClient,
  ],
})
export class AppModule {}
