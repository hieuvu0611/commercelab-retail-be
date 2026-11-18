import {
  ArrayMinSize,
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

class ReturnRequestFields {
  @IsOptional()
  @IsString()
  rmaId?: string;

  @IsOptional()
  @IsInt()
  @Min(1)
  parcelIndex?: number;

  @IsIn(VALID_REASONS)
  reason: string;

  @IsArray()
  @ArrayMinSize(1)
  @ValidateNested({ each: true })
  @Type(() => ReturnLineDto)
  lines: ReturnLineDto[];

  @IsOptional()
  @IsString()
  notes?: string;

  @IsOptional()
  isExchange?: boolean;
}

export class CreateReturnRequestDto extends ReturnRequestFields {
  @IsString()
  @IsNotEmpty()
  orderId: string;
}

export class CreateOrderReturnRequestDto extends ReturnRequestFields {
  @IsOptional()
  @IsString()
  orderId?: string;
}
