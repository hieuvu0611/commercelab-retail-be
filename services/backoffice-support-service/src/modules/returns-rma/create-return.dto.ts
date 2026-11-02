import { IsIn, IsInt, IsNotEmpty, IsOptional, IsString, Min } from 'class-validator';

export class CreateReturnDto {
  @IsOptional() @IsString() @IsNotEmpty() rmaId?: string;
  @IsString() @IsNotEmpty() orderId: string;
  @IsString() @IsNotEmpty() skuId: string;
  @IsInt() @Min(1) quantity: number;
  @IsIn(['CHANGE_OF_MIND', 'SELLER_FAULT']) reason: 'CHANGE_OF_MIND' | 'SELLER_FAULT';
}
