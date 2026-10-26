import { ConflictException, Injectable, NotFoundException } from '@nestjs/common';

export interface OrderLineReturnCeiling {
  orderId: string;
  skuId: string;
  deliveredQty: number;
  returnedAcceptedQty: number;
  returnOpenQty: number;
  allocatedUnitPaidVnd: number;
  shippingFeeVnd: number;
}

export interface RmaDossier {
  rmaId: string;
  orderId: string;
  skuId: string;
  quantity: number;
  reason: 'CHANGE_OF_MIND' | 'SELLER_FAULT';
  status: 'OPEN_PENDING_RECEIPT' | 'RECEIVED_UNSELLABLE' | 'QC_PASSED_RESTOCKED' | 'QC_FAILED_ISOLATED' | 'REJECTED';
  refundableAmountVnd: number;
  timeline: Array<{ at: string; actor: string; note: string }>;
}

@Injectable()
export class ReturnsRmaService {
  // Khởi tạo trần trả hàng mẫu theo SC01 & SC07 (2 Balo thực trả 270.000đ/chiếc, 1 Bình nước 90.000đ/chiếc)
  private readonly ceilings = new Map<string, OrderLineReturnCeiling>([
    [
      'O-001|SKU-BALO-BLACK-20L',
      {
        orderId: 'O-001',
        skuId: 'SKU-BALO-BLACK-20L',
        deliveredQty: 2,
        returnedAcceptedQty: 0,
        returnOpenQty: 0,
        allocatedUnitPaidVnd: 270_000,
        shippingFeeVnd: 30_000,
      },
    ],
  ]);

  private readonly dossiers = new Map<string, RmaDossier>();

  /**
   * Thực thi B06, P07, P09, P11, I07 & SC07:
   * - Kiểm tra và giữ hạn mức số lượng trả (I07: delivered >= accepted + open).
   * - Đổi ý (P09): Hoàn theo giá thực trả phân bổ lúc mua (270.000đ), KHÔNG hoàn phí giao 30.000đ.
   */
  openRmaRequest(
    rmaId: string,
    orderId: string,
    skuId: string,
    quantity: number,
    reason: 'CHANGE_OF_MIND' | 'SELLER_FAULT',
  ): RmaDossier {
    const key = `${orderId}|${skuId}`;
    const ceiling = this.ceilings.get(key);
    if (!ceiling) {
      throw new NotFoundException(`Không tìm thấy dòng hàng đã giao cho ${key}`);
    }

    const remainingReturnable = ceiling.deliveredQty - ceiling.returnedAcceptedQty - ceiling.returnOpenQty;
    if (quantity <= 0 || quantity > remainingReturnable) {
      throw new ConflictException(
        `Vi phạm bất biến I07: Số lượng yêu cầu trả (${quantity}) vượt hạn mức còn được trả (${remainingReturnable})`,
      );
    }

    // Khóa ngay số lượng vào returnOpenQty để chặn tạo 2 hồ sơ trả trùng trên cùng 1 chiếc
    ceiling.returnOpenQty += quantity;

    // P09 & SC07: Đổi ý hoàn 270.000đ/chiếc, không hoàn phí giao 30.000đ
    let refundableAmountVnd = quantity * ceiling.allocatedUnitPaidVnd;
    if (reason === 'SELLER_FAULT' && quantity === ceiling.deliveredQty) {
      refundableAmountVnd += ceiling.shippingFeeVnd; // P11
    }

    const dossier: RmaDossier = {
      rmaId,
      orderId,
      skuId,
      quantity,
      reason,
      status: 'OPEN_PENDING_RECEIPT',
      refundableAmountVnd,
      timeline: [
        {
          at: new Date().toISOString(),
          actor: 'CSKH',
          note: `Mở hồ sơ trả ${quantity}x ${skuId} (${reason}), giữ trần trả I07, dự kiến hoàn ${refundableAmountVnd}đ`,
        },
      ],
    };
    this.dossiers.set(rmaId, dossier);
    return dossier;
  }
}
