import {
  IsArray,
  IsIn,
  IsInt,
  IsNotEmpty,
  IsOptional,
  IsString,
  Min,
  ValidateNested,
} from 'class-validator';
import { Type } from 'class-transformer';

export class ReturnLineDto {
  @IsString()
  @IsNotEmpty()
  skuId: string;

  @IsOptional()
  @IsString()
  skuName?: string;

  @IsInt()
  @Min(1)
  quantity: number;
}

export const VALID_REASONS = [
  'CHANGED_MIND',
  'WRONG_ITEM',
  'DAMAGED',
  'DEFECTIVE',
  'NOT_AS_DESCRIBED',
  'CHANGE_OF_MIND',
  'SELLER_FAULT',
] as const;

export class CreateReturnRequestDto {
  @IsOptional()
  @IsString()
  rmaId?: string;

  @IsString()
  @IsNotEmpty()
  orderId: string;

  @IsIn(VALID_REASONS)
  reason: string;

  @IsArray()
  @ValidateNested({ each: true })
  @Type(() => ReturnLineDto)
  lines: ReturnLineDto[];

  @IsOptional()
  @IsString()
  notes?: string;

  @IsOptional()
  isExchange?: boolean;

  @IsOptional()
  @IsString()
  deliveredAt?: string;
}
