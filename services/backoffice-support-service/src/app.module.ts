import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { MakerCheckerPolicyService } from './common/guards/maker-checker.guard';
import { ReturnsRmaService } from './modules/returns-rma/returns.service';
import { ExceptionCasesService } from './modules/exception-cases/exception-cases.service';

@Module({
  imports: [],
  controllers: [AppController],
  providers: [MakerCheckerPolicyService, ReturnsRmaService, ExceptionCasesService],
})
export class AppModule {}
