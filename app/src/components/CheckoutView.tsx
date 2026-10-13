import React, { useState, useEffect } from 'react';
import { CartItem, CheckoutFormState, CustomerOrderSummary, Currency, Order, User, ShippingAddress } from '../types';
import { createMomoPaymentApi, createVnpayPaymentApi, createPaypalPaymentApi, VerifyAtmOtpResponse } from '../api/payment';
import { createCodOrderApi } from '../api/order';
import { NcbOtpModal } from './NcbOtpModal';
import { QRCodeSVG } from 'qrcode.react';

// PayPal không hỗ trợ VND → backend quy đổi sang USD (VNDPerUSD bên service)
const VND_PER_USD = 25000;

// Tài khoản thụ hưởng cố định của shop (Vietcombank)
const SHOP_BANK_BIN = '970436'; // Vietcombank
const SHOP_BANK_ACC = '88992026888';

// VietQR helpers — sinh chuỗi QR Napas 247 (động, kèm số tiền)
function vietQRField(id: string, value: string): string {
  return id + String(value.length).padStart(2, '0') + value;
}

function vietQRCrc16(str: string): string {
  let crc = 0xffff;
  for (let i = 0; i < str.length; i++) {
    crc ^= str.charCodeAt(i) << 8;
    for (let j = 0; j < 8; j++) {
      crc = crc & 0x8000 ? ((crc << 1) ^ 0x1021) & 0xffff : (crc << 1) & 0xffff;
    }
  }
  return crc.toString(16).toUpperCase().padStart(4, '0');
}

// buildVietQR — QR chuyển khoản đúng số tiền (đơn vị VND, làm tròn đồng)
function buildVietQR(bankBin: string, account: string, amountVND: number, note: string): string {
  const merchant =
    vietQRField('00', 'A000000727') +
    vietQRField('01', vietQRField('00', bankBin) + vietQRField('01', account));
  const payload =
    vietQRField('00', '01') +
    vietQRField('01', '12') + // 12 = QR động (có số tiền)
    vietQRField('38', merchant) +
    vietQRField('52', '0000') +
    vietQRField('53', '704') + // VND
    vietQRField('54', String(Math.max(0, Math.round(amountVND)))) +
    vietQRField('58', 'VN') +
    vietQRField('62', vietQRField('08', note.slice(0, 25))) +
    '6304';
  return payload + vietQRCrc16(payload);
}

interface CheckoutViewProps {
  cart: CartItem[];
  currency: Currency;
  onReturnToCart: () => void;
  onOrderPlaced: (order: Order) => void;
  appliedDiscountCode: string;
  onApplyDiscountCode: (code: string) => boolean | Promise<boolean>;
  user?: User | null;
  onOpenSettings?: (tab?: 'profile' | 'address' | 'payments') => void;
  onOpenProfile?: () => void;
  onSaveAddressToProfile?: (address: ShippingAddress) => void;
}

export const CheckoutView: React.FC<CheckoutViewProps> = ({
  cart,
  currency,
  onReturnToCart,
  onOrderPlaced,
  appliedDiscountCode,
  onApplyDiscountCode,
  user,
  onOpenSettings,
  onOpenProfile,
  onSaveAddressToProfile
}) => {
  const [formData, setFormData] = useState<CheckoutFormState>({
    email: user?.email || 'customer@boko.com',
    firstName: user?.shippingAddress?.fullName || user?.name || 'Albert Camus',
    lastName: '',
    company: '',
    address: user?.shippingAddress?.streetAddress || 'Rue Sébastien Bottin',
    apt: '',
    city: user?.shippingAddress?.province || 'Hà Nội',
    country: 'Vietnam',
    postalCode: '100000',
    telephone: user?.shippingAddress?.phone || '0912 345 678',
    province: user?.shippingAddress?.province || 'Hà Nội',
    ward: user?.shippingAddress?.ward || 'Phường Bến Nghé',
    streetAddress: user?.shippingAddress?.streetAddress || 'Số 123 Đường Đồng Khởi',
    paymentMethod: 'cod',
    cardNumber: '4532 •••• •••• 8892',
    cardExpiry: '12/28',
    cardCvv: '882',
    ewalletType: 'momo'
  });

  const [selectedSavedPaymentId, setSelectedSavedPaymentId] = useState<string | null>(null);
  const [savedSuccessMsg, setSavedSuccessMsg] = useState<string | null>(null);
  const [isSubmittingMomo, setIsSubmittingMomo] = useState<boolean>(false);
  const [momoMethod, setMomoMethod] = useState<'payWithMethod' | 'captureWallet'>('payWithMethod');
  const [isSubmittingVnpay, setIsSubmittingVnpay] = useState<boolean>(false);
  const [isSubmittingPaypal, setIsSubmittingPaypal] = useState<boolean>(false);
  const [isSubmittingAtm, setIsSubmittingAtm] = useState<boolean>(false);
  const [showNcbOtpModal, setShowNcbOtpModal] = useState<boolean>(false);
  const [vnpayMethod, setVnpayMethod] = useState<'NCB' | 'VNPAYQR' | 'VISA'>('NCB');

  const savedCards = (user?.paymentMethods || []).filter((m) => m.type === 'card');
  const savedMomo = (user?.paymentMethods || []).filter((m) => m.type === 'momo');
  const savedAtm = (user?.paymentMethods || []).filter((m) => m.type === 'atm');
  const savedZalo = (user?.paymentMethods || []).filter((m) => m.type === 'zalopay');
  const savedBanks = (user?.paymentMethods || []).filter((m) => m.type === 'bank');

  // Handle open settings tab
  const handleOpenSettingsTab = (tab: 'profile' | 'address' | 'payments') => {
    if (onOpenSettings) {
      onOpenSettings(tab);
    } else if (onOpenProfile) {
      onOpenProfile();
    }
  };

  // Auto pre-fill when user object is updated or present
  useEffect(() => {
    if (user) {
      const addr = user.shippingAddress;
      setFormData((prev) => ({
        ...prev,
        email: user.email || prev.email,
        firstName: addr?.fullName || user.name || prev.firstName,
        telephone: addr?.phone || prev.telephone,
        province: addr?.province || prev.province || 'Hà Nội',
        ward: addr?.ward || prev.ward || '',
        streetAddress: addr?.streetAddress || prev.streetAddress || prev.address,
        address: addr?.streetAddress || prev.address,
        city: addr?.province || prev.city
      }));
    }
  }, [user]);

  const [discountInput, setDiscountInput] = useState<string>('');
  const [discountMessage, setDiscountMessage] = useState<{ text: string; isError: boolean } | null>(null);
  const [copiedBank, setCopiedBank] = useState<boolean>(false);
  const [formErrors, setFormErrors] = useState<string[]>([]);

  // Default fallback book if cart is empty so user can test the exact checkout screen from Image 1!
  const displayItems: CartItem[] =
    cart.length > 0
      ? cart
      : [
          {
            book: {
              id: 'camus-1',
              title: "L'Étranger",
              author: 'Albert Camus',
              category: 'VĂN HỌC',
              priceEUR: 24.0,
              priceVND: 240000,
              coverUrl:
                'https://lh3.googleusercontent.com/aida-public/AB6AXuAMmrVi1HZtk0j2V8SVSuMmHBIUX0oNAHwkxVKk3q0h3zQQiRNTRbE-5N5Bwuhk8a1Cy0vpDMnZrIpZrx7LYQIj41lhJ6WW0hFpMWUMS-_dCJOMUqDWgl5JD31eyoLktacAZ81q668R300goQc7QRRI-YtcdZfCcQ5ePgjA9GHz0EqyWg0gZxzhJ3CeSbQQ3ZcCkAlYAfDC68av5rURiElgkcov58r30L9Bp6mQZ86SK8AGHMmccC-Z4w',
              description: 'L\'Étranger by Albert Camus',
              sampleChapters: { title: '', page1: [], page2: [], page3: [] }
            },
            quantity: 1
          }
        ];

  // Price calculations
  const subtotalEUR = displayItems.reduce((acc, i) => acc + i.book.priceEUR * i.quantity, 0);
  const subtotalVND = displayItems.reduce((acc, i) => acc + i.book.priceVND * i.quantity, 0);

  const isPercentDiscount = appliedDiscountCode.toUpperCase() === 'GIAM10';
  const isFreeShip = appliedDiscountCode.toUpperCase() === 'FREESHIP';

  const discountRate = isPercentDiscount ? 0.1 : 0;
  const discountEUR = subtotalEUR * discountRate;
  const discountVND = subtotalVND * discountRate;

  const vatEUR = 0;
  const vatVND = 0;

  const shippingEUR = 0;
  const shippingVND = 0;

  const totalEUR = Math.max(0, subtotalEUR - discountEUR);
  const totalVND = Math.max(0, subtotalVND - discountVND);

  const isEUR = currency === 'EUR';
  const totalUSD = Math.round((totalVND / VND_PER_USD) * 100) / 100;

  const buildShippingAddress = () =>
    `${formData.streetAddress || formData.address}, ${formData.ward || ''}, ${formData.province || formData.city || ''}`
      .replace(/(^,\s*|,\s*$)/g, '')
      .trim();

  const formatPrice = (eur: number, vnd: number) => {
    return isEUR ? `€ ${eur.toFixed(2)}` : `${vnd.toLocaleString('vi-VN')} ₫`;
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    const { id, value } = e.target;
    setFormData((prev) => ({ ...prev, [id]: value }));
  };

  const handleApplyDiscount = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!discountInput.trim()) return;

    const success = await onApplyDiscountCode(discountInput.trim());
    if (success) {
      setDiscountMessage({ text: `Đã áp dụng mã "${discountInput.trim().toUpperCase()}"`, isError: false });
      setDiscountInput('');
    } else {
      setDiscountMessage({ text: 'Mã giảm giá không hợp lệ. Thử GIAM10 hoặc FREESHIP', isError: true });
    }
  };

  const handleCopyAccount = () => {
    navigator.clipboard.writeText('88992026888');
    setCopiedBank(true);
    setTimeout(() => setCopiedBank(false), 2000);
  };

  const handleAtmPaymentSuccess = (res: VerifyAtmOtpResponse) => {
    const customerSummary: CustomerOrderSummary = {
      firstName: formData.firstName,
      lastName: formData.lastName,
      email: formData.email,
      telephone: formData.telephone,
      address: formData.address,
      streetAddress: formData.streetAddress,
      ward: formData.ward,
      province: formData.province,
      shippingAddress: buildShippingAddress() || 'Địa chỉ nhận sách Boko',
      paymentMethod: 'atm',
    };

    const newOrder: Order = {
      id: `BST-${res.order_id}`,
      dbId: res.order_id,
      date: new Date().toLocaleDateString('vi-VN'),
      items: displayItems,
      customer: customerSummary,
      subtotalEUR,
      subtotalVND,
      discountEUR,
      discountVND,
      vatEUR: 0,
      vatVND: 0,
      shippingEUR: 0,
      shippingVND: 0,
      totalEUR,
      totalVND: res.total ?? totalVND,
      currency,
      discountCode: appliedDiscountCode || undefined,
      status: 'shipping',
      paymentStatus: 'paid',
      paymentMethod: 'atm',
      shippingAddress: buildShippingAddress() || 'Địa chỉ nhận sách Boko',
      phone: formData.telephone,
    };

    // Lưu vào localStorage danh sách boko_orders để đồng bộ tức thì
    try {
      const saved = localStorage.getItem('boko_orders');
      const existingList = saved ? JSON.parse(saved) : [];
      const updatedList = [newOrder, ...existingList.filter((o: any) => o.id !== newOrder.id)];
      localStorage.setItem('boko_orders', JSON.stringify(updatedList));
    } catch (e) {
      console.error(e);
    }

    setShowNcbOtpModal(false);
    onOrderPlaced(newOrder);
  };

  const handleSubmitOrder = async (e: React.FormEvent) => {
    e.preventDefault();

    // Basic validation
    const errors: string[] = [];
    if (!formData.email.trim()) errors.push('Vui lòng nhập Email');
    if (!formData.firstName.trim()) errors.push('Vui lòng nhập Họ và Tên người nhận');
    if (!formData.telephone.trim()) errors.push('Vui lòng nhập Số điện thoại');
    if (!(formData.streetAddress || formData.address || '').trim()) errors.push('Vui lòng nhập Địa chỉ giao hàng');

    if (errors.length > 0) {
      setFormErrors(errors);
      return;
    }

    // Check non-COD payment integrations
    if (formData.paymentMethod === 'card' && savedCards.length === 0) {
      setFormErrors(['Bạn chưa tích hợp Thẻ tín dụng/ghi nợ trong Cài đặt. Vui lòng thêm thẻ thanh toán trước khi tiếp tục.']);
      handleOpenSettingsTab('payments');
      return;
    }

    if (formData.paymentMethod === 'atm' && savedAtm.length === 0) {
      setFormErrors(['Bạn chưa liên kết Thẻ ATM nội địa trong Cài đặt. Vui lòng liên kết thẻ trước khi tiếp tục.']);
      handleOpenSettingsTab('payments');
      return;
    }

    if (formData.paymentMethod === 'ewallet') {
      if (formData.ewalletType === 'zalopay' && savedZalo.length === 0) {
        setFormErrors(['Bạn chưa liên kết Ví ZaloPay trong Cài đặt. Vui lòng liên kết ví trước khi tiếp tục.']);
        handleOpenSettingsTab('payments');
        return;
      }
    }

    setFormErrors([]);

    // Nếu chọn thanh toán qua Cổng MoMo Sandbox -> Tạo phiên và chuyển hướng sang trang MoMo
    if (formData.paymentMethod === 'ewallet' && formData.ewalletType === 'momo') {
      setIsSubmittingMomo(true);
      try {
        const fullAddress = `${formData.streetAddress || formData.address || ''}, ${formData.ward || ''}, ${formData.province || ''}`.trim();
        const res = await createMomoPaymentApi({
          amount: Math.round(totalVND),
          shippingAddress: fullAddress || 'Địa chỉ nhận sách Boko',
          phone: formData.telephone,
          email: formData.email,
          redirectUrl: `${window.location.origin}/payment/momo-callback`,
          requestType: momoMethod,
        });

        if (res && res.pay_url) {
          window.location.href = res.pay_url;
          return;
        } else {
          throw new Error('Không nhận được đường link pay_url từ MoMo Sandbox.');
        }
      } catch (err: any) {
        setIsSubmittingMomo(false);
        setFormErrors([err.message || 'Không thể tạo phiên thanh toán MoMo Sandbox. Vui lòng kiểm tra lại.']);
        return;
      }
    }

    // Nếu chọn thanh toán qua Cổng VNPAY Sandbox -> Tạo phiên và chuyển hướng sang cổng VNPAY
    if (formData.paymentMethod === 'ewallet' && formData.ewalletType === 'vnpay') {
      setIsSubmittingVnpay(true);
      try {
        const fullAddress = `${formData.streetAddress || formData.address || ''}, ${formData.ward || ''}, ${formData.province || ''}`.trim();
        const res = await createVnpayPaymentApi({
          amount: Math.round(totalVND),
          shippingAddress: fullAddress || 'Địa chỉ nhận sách Boko',
          phone: formData.telephone,
          email: formData.email,
          bankCode: vnpayMethod,
          redirectUrl: `${window.location.origin}/payment/vnpay-callback`,
        });

        if (res && res.payment_url) {
          window.location.href = res.payment_url;
          return;
        } else {
          throw new Error('Không nhận được đường link payment_url từ VNPAY Sandbox.');
        }
      } catch (err: any) {
        setIsSubmittingVnpay(false);
        setFormErrors([err.message || 'Không thể tạo phiên thanh toán VNPAY Sandbox. Vui lòng kiểm tra lại.']);
        return;
      }
    }

    // Nếu chọn thanh toán qua PayPal -> Tạo đơn PayPal rồi chuyển hướng sang PayPal duyệt.
    // PayPal duyệt xong redirect về /payment/paypal-callback để xác nhận và lưu đơn.
    if (formData.paymentMethod === 'paypal') {
      setIsSubmittingPaypal(true);
      try {
        const res = await createPaypalPaymentApi({
          amount: Math.round(totalVND),
          shippingAddress: buildShippingAddress() || 'Địa chỉ nhận sách Boko',
          phone: formData.telephone,
          email: formData.email,
          redirectUrl: `${window.location.origin}/payment/paypal-callback`,
        });

        if (res && res.approve_url) {
          window.location.href = res.approve_url;
          return;
        } else {
          throw new Error('Không nhận được link duyệt PayPal.');
        }
      } catch (err: unknown) {
        setIsSubmittingPaypal(false);
        setFormErrors([(err as Error)?.message || 'Không thể tạo phiên thanh toán PayPal. Vui lòng kiểm tra lại.']);
        return;
      }
    }

    // Nếu chọn thanh toán qua Thẻ ATM Nội Địa -> Mở Modal Xác Thực OTP NCB 1-chạm (Không chuyển sang VNPAY ngoài)
    if (formData.paymentMethod === 'atm') {
      setShowNcbOtpModal(true);
      return;
    }

    let createdDbId: number | undefined;
    const orderStatus: 'shipping' | 'pending' = 'shipping';
    const paymentStatus: 'unpaid' | 'paid' = 'unpaid';

    let serverConfirmedTotal = totalVND;

    if (formData.paymentMethod === 'cod') {
      try {
        const fullAddress = `${formData.streetAddress || formData.address || ''}, ${formData.ward || ''}, ${formData.province || ''}`.trim();
        const idempotencyKey = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `IDEMP-${Date.now()}-${Math.random().toString(36).substring(2, 9)}`;

        const codItems = displayItems.map((item) => {
          const parsed = Number(item.book.id);
          const numId = Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 1;
          return {
            book_id: numId,
            title: item.book.title,
            price: item.book.priceVND,
            quantity: item.quantity,
          };
        });

        const codRes = await createCodOrderApi({
          amount: Math.round(totalVND),
          shippingAddress: fullAddress || 'Địa chỉ nhận sách Boko',
          phone: formData.telephone,
          email: formData.email,
          customerName: `${formData.firstName} ${formData.lastName}`.trim(),
          couponCode: appliedDiscountCode || '',
          idempotencyKey,
          items: codItems,
        });

        if (codRes && codRes.order_id) {
          createdDbId = codRes.order_id;
          if (typeof codRes.total === 'number') {
            serverConfirmedTotal = codRes.total;
          }
        } else {
          throw new Error('Không nhận được mã xác nhận đơn hàng từ server.');
        }
      } catch (err: any) {
        console.error('Lỗi lưu đơn COD vào backend:', err);
        setFormErrors([err.message || 'Không thể tạo đơn hàng COD. Vui lòng kiểm tra lại.']);
        return; // DỪNG TIẾN TRÌNH: TUYỆT ĐỐI KHÔNG FAKE SUCCESS!
      }
    }

    const customerSummary: CustomerOrderSummary = {
      firstName: formData.firstName,
      lastName: formData.lastName,
      email: formData.email,
      telephone: formData.telephone,
      address: formData.address,
      streetAddress: formData.streetAddress,
      ward: formData.ward,
      province: formData.province,
      shippingAddress: `${formData.streetAddress || formData.address || ''}, ${formData.ward || ''}, ${formData.province || ''}`.trim(),
      paymentMethod: formData.paymentMethod,
    };

    const newOrder: Order = {
      id: createdDbId ? `BST-${createdDbId}` : `BST-${Math.floor(100000 + Math.random() * 900000)}`,
      dbId: createdDbId,
      date: new Date().toLocaleDateString('vi-VN'),
      items: displayItems,
      customer: customerSummary,
      subtotalEUR,
      subtotalVND,
      discountEUR,
      discountVND,
      vatEUR: 0,
      vatVND: 0,
      shippingEUR: 0,
      shippingVND: 0,
      totalEUR,
      totalVND: serverConfirmedTotal,
      currency,
      discountCode: appliedDiscountCode || undefined,
      status: orderStatus,
      paymentStatus: paymentStatus,
      paymentMethod: formData.paymentMethod,
      shippingAddress: `${formData.streetAddress || formData.address || ''}, ${formData.ward || ''}, ${formData.province || ''}`.trim(),
      phone: formData.telephone,
    };

    // Lưu vào localStorage danh sách boko_orders để đồng bộ tức thì
    try {
      const saved = localStorage.getItem('boko_orders');
      const existingList = saved ? JSON.parse(saved) : [];
      const updatedList = [newOrder, ...existingList.filter((o: any) => o.id !== newOrder.id)];
      localStorage.setItem('boko_orders', JSON.stringify(updatedList));
    } catch (e) {
      console.error(e);
    }

    onOrderPlaced(newOrder);
  };

  return (
    <div className="w-full min-h-screen bg-slate-50 text-slate-900">
      {/* Progress Indicator Stepper Header */}
      <div className="w-full pt-8 pb-6 flex justify-center items-center space-x-2 sm:space-x-6 px-4 sm:px-16">
        <button
          onClick={onReturnToCart}
          className="flex items-center text-slate-500 hover:text-slate-900 transition-colors cursor-pointer"
        >
          <span className="font-label-caps text-xs tracking-wider uppercase font-medium">Cart</span>
        </button>
        <div className="w-6 sm:w-16 h-px bg-slate-200"></div>
        <div className="flex items-center bg-blue-50 text-blue-700 px-3 py-1 rounded-full font-bold">
          <span className="font-label-caps text-xs tracking-widest uppercase">Customer Info</span>
        </div>
        <div className="w-6 sm:w-16 h-px bg-slate-200"></div>
        <div className="flex items-center text-slate-400">
          <span className="font-label-caps text-xs tracking-wider uppercase">Shipping</span>
        </div>
        <div className="w-6 sm:w-16 h-px bg-slate-200"></div>
        <div className="flex items-center text-slate-400">
          <span className="font-label-caps text-xs tracking-wider uppercase">Payment</span>
        </div>
      </div>

      {/* Main Content Area - Form & Summary Desk Grid */}
      <main className="max-w-7xl mx-auto px-4 sm:px-8 lg:px-16 py-6 lg:py-10 grid grid-cols-1 lg:grid-cols-12 gap-10 lg:gap-16 items-start">
        {/* Left Column: Input Forms */}
        <section className="lg:col-span-7 space-y-10">
          {formErrors.length > 0 && (
            <div className="p-4 bg-red-50 border border-red-200 text-red-700 text-xs rounded-xl space-y-1">
              <p className="font-bold">Vui lòng kiểm tra lại thông tin:</p>
              <ul className="list-disc list-inside">
                {formErrors.map((err, idx) => (
                  <li key={idx}>{err}</li>
                ))}
              </ul>
            </div>
          )}

          {/* Customer Info Section */}
          <div className="space-y-4">
            <h2 className="font-display font-bold text-xl sm:text-2xl text-slate-900 border-b border-slate-200 pb-3">
              Thông Tin Khách Hàng
            </h2>
            <div className="pt-2">
              <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="email">
                Địa chỉ Email
              </label>
              <input
                id="email"
                type="email"
                value={formData.email}
                onChange={handleInputChange}
                placeholder="email@example.com"
                className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
              />
            </div>
          </div>

          {/* Shipping Address Section */}
          <div className="space-y-6">
            <div className="flex items-center justify-between border-b border-slate-200 pb-3">
              <h2 className="font-display font-bold text-xl sm:text-2xl text-slate-900">
                Địa Chỉ Nhận Hàng
              </h2>
              {user && (
                <button
                  type="button"
                  onClick={onOpenProfile}
                  className="text-xs font-semibold text-amber-900 hover:text-amber-700 flex items-center gap-1 bg-amber-50 hover:bg-amber-100 px-3 py-1.5 rounded-lg border border-amber-200/80 transition-colors cursor-pointer"
                >
                  <i className="fa-solid fa-location-dot text-xs"></i>
                  <span>Cấu hình Hồ sơ</span>
                </button>
              )}
            </div>

            {/* Profile Auto-fill Banner / Feedback */}
            {user?.shippingAddress ? (
              <div className="p-3.5 bg-emerald-50 border border-emerald-200/90 rounded-2xl flex items-center justify-between text-xs text-emerald-900 font-medium animate-fadeIn">
                <div className="flex items-center gap-2">
                  <i className="fa-solid fa-circle-check text-emerald-600 text-sm"></i>
                  <span>
                    Đã tự động điền địa chỉ giao hàng từ Hồ sơ cá nhân của <strong>{user.name}</strong>
                  </span>
                </div>
                <button
                  type="button"
                  onClick={onOpenProfile}
                  className="text-emerald-800 underline hover:text-emerald-950 font-bold ml-2 whitespace-nowrap cursor-pointer"
                >
                  Thay đổi
                </button>
              </div>
            ) : user ? (
              <div className="p-3 bg-amber-50/90 border border-amber-200/80 rounded-2xl flex items-center justify-between text-xs text-amber-900 font-medium">
                <div className="flex items-center gap-2">
                  <i className="fa-solid fa-circle-info text-amber-600 text-sm"></i>
                  <span>Lưu địa chỉ bên dưới vào Hồ sơ cá nhân để không cần điền lại ở những lần mua sau.</span>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    if (onSaveAddressToProfile) {
                      const newAddr: ShippingAddress = {
                        fullName: formData.firstName,
                        phone: formData.telephone,
                        province: formData.province || formData.city || 'Hà Nội',
                        ward: formData.ward || '',
                        streetAddress: formData.streetAddress || formData.address
                      };
                      onSaveAddressToProfile(newAddr);
                      setSavedSuccessMsg('Đã lưu địa chỉ vào Hồ sơ cá nhân!');
                      setTimeout(() => setSavedSuccessMsg(null), 3000);
                    }
                  }}
                  className="px-3 py-1 bg-amber-900 hover:bg-amber-800 text-amber-50 font-semibold rounded-lg text-xs transition-colors shadow-2xs whitespace-nowrap ml-2 cursor-pointer"
                >
                  Lưu vào Hồ sơ
                </button>
              </div>
            ) : null}

            {savedSuccessMsg && (
              <div className="p-3 bg-emerald-50 border border-emerald-200 rounded-xl text-xs font-semibold text-emerald-800 flex items-center gap-2">
                <i className="fa-solid fa-circle-check text-emerald-600 text-sm"></i>
                <span>{savedSuccessMsg}</span>
              </div>
            )}

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-6 pt-1">
              {/* Tên người nhận */}
              <div>
                <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="firstName">
                  Tên người nhận <span className="text-red-500">*</span>
                </label>
                <input
                  id="firstName"
                  type="text"
                  value={formData.firstName}
                  onChange={handleInputChange}
                  placeholder="Họ và Tên người nhận"
                  className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
                />
              </div>

              {/* Số điện thoại */}
              <div>
                <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="telephone">
                  Số điện thoại <span className="text-red-500">*</span>
                </label>
                <input
                  id="telephone"
                  type="tel"
                  value={formData.telephone}
                  onChange={handleInputChange}
                  placeholder="Số điện thoại nhận hàng"
                  className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
                />
              </div>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-6">
              {/* Tỉnh / Thành phố */}
              <div>
                <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="province">
                  Tỉnh / Thành phố <span className="text-red-500">*</span>
                </label>
                <input
                  id="province"
                  type="text"
                  value={formData.province || formData.city}
                  onChange={(e) => {
                    const val = e.target.value;
                    setFormData((prev) => ({ ...prev, province: val, city: val }));
                  }}
                  placeholder="TP. Hồ Chí Minh, Hà Nội, ĐN..."
                  className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
                />
              </div>

              {/* Phường / Xã */}
              <div>
                <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="ward">
                  Phường / Xã <span className="text-red-500">*</span>
                </label>
                <input
                  id="ward"
                  type="text"
                  value={formData.ward || ''}
                  onChange={(e) => {
                    const val = e.target.value;
                    setFormData((prev) => ({ ...prev, ward: val }));
                  }}
                  placeholder="Phường Bến Nghé, Quận 1..."
                  className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
                />
              </div>
            </div>

            {/* Tên đường / Tòa nhà / Số nhà */}
            <div>
              <label className="block text-xs font-bold text-slate-700 mb-1" htmlFor="streetAddress">
                Tên đường / Tòa nhà / Số nhà <span className="text-red-500">*</span>
              </label>
              <input
                id="streetAddress"
                type="text"
                value={formData.streetAddress || formData.address}
                onChange={(e) => {
                  const val = e.target.value;
                  setFormData((prev) => ({ ...prev, streetAddress: val, address: val }));
                }}
                placeholder="Số 123 Đường Đồng Khởi, Tòa nhà Vincom"
                className="paper-input w-full font-body text-sm text-slate-900 placeholder:text-slate-400"
              />
            </div>
          </div>

          {/* Payment Method Section */}
          <div className="space-y-6 pt-4">
            <h2 className="font-display font-bold text-xl sm:text-2xl text-slate-900 border-b border-slate-200 pb-3">
              Payment Method
            </h2>

            <div className="space-y-5">
              {/* Cash On Delivery (COD) Option */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-cod"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'cod'}
                    onChange={() => setFormData((p) => ({ ...p, paymentMethod: 'cod' }))}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-cod" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    Thanh toán khi nhận hàng (COD)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-solid fa-truck-fast text-slate-400 text-base"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'cod' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-2">
                    <p className="text-xs text-slate-600 font-body">
                      Bạn sẽ thanh toán trực tiếp bằng tiền mặt cho nhân viên giao hàng khi nhận được sách.
                    </p>
                    <div className="bg-amber-50 border border-amber-200/80 p-3 rounded-lg text-xs text-amber-800 flex items-center gap-2">
                      <i className="fa-solid fa-circle-info text-amber-600 text-sm"></i>
                      <span>Vui lòng kiểm tra kỹ thông tin đơn hàng và chuẩn bị số tiền tương ứng khi nhận hàng.</span>
                    </div>
                  </div>
                )}
              </div>

              {/* Credit / Debit Card Option */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-card"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'card'}
                    onChange={() => {
                      setFormData((p) => ({ ...p, paymentMethod: 'card' }));
                      if (savedCards.length === 0) {
                        handleOpenSettingsTab('payments');
                      }
                    }}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-card" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    Thẻ Quốc Tế / Thẻ Tín Dụng (Visa, Mastercard, JCB)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-solid fa-credit-card text-slate-400 text-base"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'card' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-3">
                    {savedCards.length > 0 ? (
                      <div className="space-y-2">
                        <p className="text-xs font-bold text-slate-700">
                          Thẻ đã tích hợp sẵn trong Cài đặt:
                        </p>
                        <div className="space-y-2">
                          {savedCards.map((card) => (
                            <div
                              key={card.id}
                              className={`p-3 rounded-xl border flex items-center justify-between gap-3 ${
                                card.isDefault || selectedSavedPaymentId === card.id
                                    ? 'bg-blue-50/60 border-blue-500'
                                  : 'bg-white border-slate-200'
                              }`}
                            >
                              <div className="flex items-center gap-2.5">
                                <i className="fa-solid fa-credit-card text-blue-600 text-base"></i>
                                <div>
                                  <p className="text-xs font-bold text-slate-900">{card.label}</p>
                                  <p className="text-[11px] text-slate-500">Chủ thẻ: {card.accountHolder}</p>
                                </div>
                              </div>
                              {card.isDefault && (
                                <span className="text-[10px] font-bold px-2 py-0.5 rounded-full bg-blue-600 text-white">
                                  Mặc định
                                </span>
                              )}
                            </div>
                          ))}
                        </div>
                        <button
                          type="button"
                          onClick={() => handleOpenSettingsTab('payments')}
                          className="text-xs font-semibold text-blue-600 hover:underline flex items-center gap-1.5 mt-1 cursor-pointer"
                        >
                          <i className="fa-solid fa-gear text-xs"></i>
                          <span>Quản lý hoặc thêm thẻ khác trong Cài đặt</span>
                        </button>
                      </div>
                    ) : (
                      <div className="p-4 rounded-xl bg-amber-50 border border-amber-200/90 text-amber-900 space-y-3">
                        <div className="flex items-start gap-2.5">
                          <i className="fa-solid fa-triangle-exclamation text-amber-700 text-lg shrink-0 mt-0.5"></i>
                          <div>
                            <p className="text-xs font-bold text-amber-950">
                              Chưa có thẻ nào được tích hợp trong tài khoản
                            </p>
                            <p className="text-xs text-amber-800/90 mt-0.5">
                              Để thanh toán bằng thẻ, vui lòng tích hợp thẻ ngân hàng / thẻ tín dụng của bạn tại trang Cài đặt.
                            </p>
                          </div>
                        </div>
                        <button
                          type="button"
                          onClick={() => handleOpenSettingsTab('payments')}
                          className="w-full sm:w-auto px-4 py-2 rounded-lg bg-amber-900 hover:bg-amber-800 text-white font-bold text-xs transition-all shadow-xs flex items-center justify-center gap-1.5 cursor-pointer"
                        >
                          <i className="fa-solid fa-credit-card text-xs"></i>
                          <span>Mở Cài Đặt Để Tích Hợp Thẻ Ngay</span>
                        </button>
                      </div>
                    )}
                  </div>
                )}
              </div>

              {/* ATM Nội Địa Option (mock — liên kết trong Cài đặt, không trừ tiền thật) */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-atm"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'atm'}
                    onChange={() => {
                      setFormData((p) => ({ ...p, paymentMethod: 'atm' }));
                      if (savedAtm.length === 0) {
                        handleOpenSettingsTab('payments');
                      }
                    }}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-atm" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    Thẻ ATM Nội Địa (Napas)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-solid fa-building-columns text-slate-400 text-base"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'atm' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-3">
                    {savedAtm.length > 0 ? (
                      <div className="space-y-2">
                        <p className="text-xs font-bold text-slate-700">
                          Thẻ ATM đã liên kết:
                        </p>
                        <div className="space-y-2">
                          {savedAtm.map((method) => (
                            <div
                              key={method.id}
                              className={`p-3 rounded-xl border flex items-center justify-between gap-3 ${
                                method.isDefault || selectedSavedPaymentId === method.id
                                  ? 'bg-teal-50/60 border-teal-600'
                                  : 'bg-white border-slate-200'
                              }`}
                            >
                              <div className="flex items-center gap-2.5">
                                <i className="fa-solid fa-building-columns text-teal-700 text-base"></i>
                                <div>
                                  <p className="text-xs font-bold text-slate-900">{method.label}</p>
                                  <p className="text-[11px] text-slate-500">Chủ thẻ: {method.accountHolder}</p>
                                </div>
                              </div>
                              {method.isDefault && (
                                <span className="text-[10px] font-bold px-2 py-0.5 rounded-full bg-teal-700 text-white">
                                  Mặc định
                                </span>
                              )}
                            </div>
                          ))}
                        </div>
                        <button
                          type="button"
                          onClick={() => handleOpenSettingsTab('payments')}
                          className="text-xs font-semibold text-blue-600 hover:underline flex items-center gap-1.5 mt-1 cursor-pointer"
                        >
                          <i className="fa-solid fa-gear text-xs"></i>
                          <span>Quản lý thẻ ATM trong Cài đặt</span>
                        </button>
                      </div>
                    ) : (
                      <div className="p-4 rounded-xl bg-amber-50 border border-amber-200/90 text-amber-900 space-y-3">
                        <div className="flex items-start gap-2.5">
                          <i className="fa-solid fa-triangle-exclamation text-amber-700 text-lg shrink-0 mt-0.5"></i>
                          <div>
                            <p className="text-xs font-bold text-amber-950">
                              Chưa có thẻ ATM nào được liên kết
                            </p>
                            <p className="text-xs text-amber-800/90 mt-0.5">
                              Vui lòng liên kết thẻ ATM nội địa của bạn tại trang Cài đặt trước khi thanh toán.
                            </p>
                          </div>
                        </div>
                        <button
                          type="button"
                          onClick={() => handleOpenSettingsTab('payments')}
                          className="w-full sm:w-auto px-4 py-2 rounded-lg bg-teal-700 hover:bg-teal-800 text-white font-bold text-xs transition-all shadow-xs flex items-center justify-center gap-1.5 cursor-pointer"
                        >
                          <i className="fa-solid fa-building-columns text-xs"></i>
                          <span>Mở Cài Đặt Để Liên Kết Thẻ ATM</span>
                        </button>
                      </div>
                    )}
                  </div>
                )}
              </div>

              {/* E-wallets Option */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-ewallet"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'ewallet'}
                    onChange={() => {
                      setFormData((p) => ({ ...p, paymentMethod: 'ewallet' }));
                    }}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-ewallet" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    Ví Điện Tử (MoMo,VNPay, ZaloPay)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-solid fa-wallet text-slate-400 text-base"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'ewallet' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-3">
                    <div className="flex gap-2 p-1 bg-slate-100 rounded-xl">
                      <button
                        type="button"
                        onClick={() => setFormData((p) => ({ ...p, ewalletType: 'momo' }))}
                        className={`flex-1 py-2 px-3 rounded-lg border text-xs font-bold uppercase transition-all cursor-pointer ${
                          formData.ewalletType === 'momo'
                            ? 'bg-[#a50064] text-white border-[#a50064] shadow-sm'
                            : 'bg-white text-slate-700 border-slate-200 hover:bg-slate-50'
                        }`}
                      >
                        Ví MoMo
                      </button>
                      <button
                        type="button"
                        onClick={() => setFormData((p) => ({ ...p, ewalletType: 'vnpay' }))}
                        className={`flex-1 py-2 px-3 rounded-lg border text-xs font-bold uppercase transition-all cursor-pointer flex items-center justify-center gap-1.5 ${
                          formData.ewalletType === 'vnpay'
                            ? 'bg-blue-700 text-white border-blue-700 shadow-sm'
                            : 'bg-white text-slate-700 border-slate-200 hover:bg-slate-50'
                        }`}
                      >
                        <span>VNPAY</span>
                        <span className="text-[10px] px-1 py-0.2 rounded font-semibold bg-amber-400 text-slate-900">Live</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => setFormData((p) => ({ ...p, ewalletType: 'zalopay' }))}
                        className={`flex-1 py-2 px-3 rounded-lg border text-xs font-bold uppercase transition-all cursor-pointer ${
                          formData.ewalletType === 'zalopay'
                            ? 'bg-blue-600 text-white border-blue-600 shadow-sm'
                            : 'bg-white text-slate-700 border-slate-200 hover:bg-slate-50'
                        }`}
                      >
                        ZaloPay
                      </button>
                    </div>

                    {formData.ewalletType === 'momo' && (
                      <div className="space-y-3 pt-1">
                        <p className="text-xs font-bold text-slate-700">
                          Chọn phương thức qua cổng MoMo:
                        </p>
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                          {/* Option 1: Pay with Method (Web: Visa, ATM, Wallet) */}
                          <div
                            onClick={() => setMomoMethod('payWithMethod')}
                            className={`p-3.5 rounded-xl border cursor-pointer transition-all ${
                              momoMethod === 'payWithMethod'
                                ? 'bg-pink-50/70 border-[#a50064] ring-1 ring-[#a50064]'
                                : 'bg-white border-slate-200 hover:border-slate-300'
                            }`}
                          >
                            <div className="flex items-start gap-2.5">
                              <input
                                type="radio"
                                id="momo-method"
                                name="momoMethodChoice"
                                checked={momoMethod === 'payWithMethod'}
                                onChange={() => setMomoMethod('payWithMethod')}
                                className="mt-0.5 text-[#a50064] focus:ring-[#a50064] border-slate-300 cursor-pointer"
                              />
                              <div>
                                <label
                                  htmlFor="momo-method"
                                  className="text-xs font-bold text-slate-900 cursor-pointer block"
                                >
                                  Cổng MoMo Web (VISA / ATM / Ví)
                                </label>
                                <p className="text-[11px] text-slate-500 mt-0.5 leading-snug">
                                  Thanh toán bằng Thẻ VISA, ATM hoặc ví trực tiếp trên Cổng MoMo
                                </p>
                              </div>
                            </div>
                          </div>

                          {/* Option 2: Scan QR Code */}
                          <div
                            onClick={() => setMomoMethod('captureWallet')}
                            className={`p-3.5 rounded-xl border cursor-pointer transition-all ${
                              momoMethod === 'captureWallet'
                                ? 'bg-pink-50/70 border-[#a50064] ring-1 ring-[#a50064]'
                                : 'bg-white border-slate-200 hover:border-slate-300'
                            }`}
                          >
                            <div className="flex items-start gap-2.5">
                              <input
                                type="radio"
                                id="momo-qr"
                                name="momoMethodChoice"
                                checked={momoMethod === 'captureWallet'}
                                onChange={() => setMomoMethod('captureWallet')}
                                className="mt-0.5 text-[#a50064] focus:ring-[#a50064] border-slate-300 cursor-pointer"
                              />
                              <div>
                                <label
                                  htmlFor="momo-qr"
                                  className="text-xs font-bold text-slate-900 cursor-pointer block"
                                >
                                  Quét mã QR MoMo (App)
                                </label>
                                <p className="text-[11px] text-slate-500 mt-0.5 leading-snug">
                                  Hiển thị mã QR trên Cổng MoMo để quét bằng app Ví MoMo
                                </p>
                              </div>
                            </div>
                          </div>
                        </div>
                      </div>
                    )}

                    {formData.ewalletType === 'vnpay' && (
                      <div className="space-y-3 pt-1 animate-fadeIn">
                        <div className="flex items-center justify-between">
                          <p className="text-xs font-bold text-slate-700">
                            Phương thức qua Cổng VNPAY Sandbox:
                          </p>
                          <span className="text-[10px] text-blue-700 font-semibold bg-blue-100 px-2 py-0.5 rounded-full">
                            HMAC-SHA512 Security
                          </span>
                        </div>

                        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2.5">
                          {/* Option 1: NCB ATM Card */}
                          <div
                            onClick={() => setVnpayMethod('NCB')}
                            className={`p-3 rounded-xl border cursor-pointer transition-all ${
                              vnpayMethod === 'NCB'
                                ? 'bg-blue-50/70 border-blue-600 ring-1 ring-blue-600'
                                : 'bg-white border-slate-200 hover:border-slate-300'
                            }`}
                          >
                            <div className="flex items-start gap-2">
                              <input
                                type="radio"
                                id="vnpay-atm"
                                name="vnpayMethodChoice"
                                checked={vnpayMethod === 'NCB'}
                                onChange={() => setVnpayMethod('NCB')}
                                className="mt-0.5 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                              />
                              <div>
                                <label
                                  htmlFor="vnpay-atm"
                                  className="text-xs font-bold text-slate-900 cursor-pointer block"
                                >
                                  🏦 Thẻ ATM (NCB)
                                </label>
                                <p className="text-[11px] text-slate-500 mt-0.5 leading-snug">
                                  Thẻ ATM ngân hàng NCB test
                                </p>
                              </div>
                            </div>
                          </div>

                          {/* Option 2: VNPAY-QR */}
                          <div
                            onClick={() => setVnpayMethod('VNPAYQR')}
                            className={`p-3 rounded-xl border cursor-pointer transition-all ${
                              vnpayMethod === 'VNPAYQR'
                                ? 'bg-blue-50/70 border-blue-600 ring-1 ring-blue-600'
                                : 'bg-white border-slate-200 hover:border-slate-300'
                            }`}
                          >
                            <div className="flex items-start gap-2">
                              <input
                                type="radio"
                                id="vnpay-qr"
                                name="vnpayMethodChoice"
                                checked={vnpayMethod === 'VNPAYQR'}
                                onChange={() => setVnpayMethod('VNPAYQR')}
                                className="mt-0.5 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                              />
                              <div>
                                <label
                                  htmlFor="vnpay-qr"
                                  className="text-xs font-bold text-slate-900 cursor-pointer block"
                                >
                                  📱 VNPAY-QR
                                </label>
                                <p className="text-[11px] text-slate-500 mt-0.5 leading-snug">
                                  Quét mã trên App Ngân Hàng
                                </p>
                              </div>
                            </div>
                          </div>

                          {/* Option 3: VISA / MasterCard */}
                          <div
                            onClick={() => setVnpayMethod('VISA')}
                            className={`p-3 rounded-xl border cursor-pointer transition-all ${
                              vnpayMethod === 'VISA'
                                ? 'bg-blue-50/70 border-blue-600 ring-1 ring-blue-600'
                                : 'bg-white border-slate-200 hover:border-slate-300'
                            }`}
                          >
                            <div className="flex items-start gap-2">
                              <input
                                type="radio"
                                id="vnpay-visa"
                                name="vnpayMethodChoice"
                                checked={vnpayMethod === 'VISA'}
                                onChange={() => setVnpayMethod('VISA')}
                                className="mt-0.5 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                              />
                              <div>
                                <label
                                  htmlFor="vnpay-visa"
                                  className="text-xs font-bold text-slate-900 cursor-pointer block"
                                >
                                  💳 Thẻ Quốc Tế
                                </label>
                                <p className="text-[11px] text-slate-500 mt-0.5 leading-snug">
                                  Thẻ Visa, MasterCard, JCB
                                </p>
                              </div>
                            </div>
                          </div>
                        </div>
                      </div>
                    )}

                    {formData.ewalletType === 'zalopay' && (
                      savedZalo.length > 0 ? (
                        <div className="p-3 rounded-xl bg-blue-50 border border-blue-200 space-y-1">
                          <p className="text-xs font-bold text-blue-950 flex items-center gap-1.5">
                            <i className="fa-solid fa-circle-check text-blue-700 text-sm"></i>
                            <span>Ví ZaloPay đã liên kết: {savedZalo[0].accountNumber}</span>
                          </p>
                          <p className="text-[11px] text-blue-800">Chủ tài khoản: {savedZalo[0].accountHolder}</p>
                        </div>
                      ) : (
                        <div className="p-4 rounded-xl bg-blue-50 border border-blue-200 space-y-3 text-blue-950">
                          <div className="flex items-start gap-2">
                            <i className="fa-solid fa-circle-info text-blue-700 text-base shrink-0 mt-0.5"></i>
                            <div>
                              <p className="text-xs font-bold">Chưa liên kết Ví ZaloPay trong Cài đặt</p>
                              <p className="text-xs text-blue-900 mt-0.5">
                                Tích hợp ví ZaloPay trong Cài đặt để tự động thanh toán đơn hàng.
                              </p>
                            </div>
                          </div>
                          <button
                            type="button"
                            onClick={() => handleOpenSettingsTab('payments')}
                            className="w-full sm:w-auto px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-700 text-white font-bold text-xs transition-all shadow-xs flex items-center justify-center gap-1.5 cursor-pointer"
                          >
                            <i className="fa-solid fa-link text-xs"></i>
                            <span>Mở Cài Đặt Để Liên Kết Ví ZaloPay</span>
                          </button>
                        </div>
                      )
                    )}
                  </div>
                )}
              </div>

              {/* PayPal Option (thu thật bằng redirect) */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-paypal"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'paypal'}
                    onChange={() => {
                      setFormData((p) => ({ ...p, paymentMethod: 'paypal' }));
                    }}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-paypal" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    PayPal (thanh toán quốc tế)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-brands fa-paypal text-[#003087] text-xl"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'paypal' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-3">
                    <div className="p-3 rounded-xl bg-blue-50 border border-blue-200 text-xs text-blue-900 flex items-center gap-2">
                      <i className="fa-brands fa-paypal text-[#003087] text-base"></i>
                      <span>
                        Tổng thanh toán qua PayPal:{' '}
                        <strong className="text-slate-900">${totalUSD.toFixed(2)} USD</strong>{' '}
                        <span className="text-slate-500">(≈ {totalVND.toLocaleString('vi-VN')} ₫)</span>.
                        Bấm Continue để mở PayPal duyệt và trả tiền.
                      </span>
                    </div>
                  </div>
                )}
              </div>

              {/* Bank Transfer Option */}
              <div className="p-4 bg-white rounded-xl border border-slate-200 space-y-4">
                <div className="flex items-center gap-3">
                  <input
                    type="radio"
                    id="payment-bank"
                    name="paymentMethod"
                    checked={formData.paymentMethod === 'bank'}
                    onChange={() => setFormData((p) => ({ ...p, paymentMethod: 'bank' }))}
                    className="w-4 h-4 text-blue-600 focus:ring-blue-500 border-slate-300 cursor-pointer"
                  />
                  <label htmlFor="payment-bank" className="font-body text-base font-semibold text-slate-900 cursor-pointer">
                    Chuyển Khoản Ngân Hàng (QR Code / Internet Banking)
                  </label>
                  <div className="flex gap-2 ml-auto">
                    <i className="fa-solid fa-building-columns text-slate-400 text-base"></i>
                  </div>
                </div>

                {formData.paymentMethod === 'bank' && (
                  <div className="pt-2 border-t border-slate-100 animate-fadeIn space-y-4">
                    {savedBanks.length > 0 && (
                      <div className="p-3 rounded-xl bg-emerald-50 border border-emerald-200 space-y-1">
                        <p className="text-xs font-bold text-emerald-950 flex items-center gap-1.5">
                          <i className="fa-solid fa-building-columns text-emerald-700 text-sm"></i>
                          <span>Tài khoản ngân hàng của bạn: {savedBanks[0].label}</span>
                        </p>
                        <p className="text-[11px] text-emerald-800">Chủ tài khoản: {savedBanks[0].accountHolder}</p>
                      </div>
                    )}

                    <div className="relative overflow-hidden rounded-2xl border border-emerald-900/20 shadow-md">
                      {/* Nền thẻ: gradient xanh VCB + họa tiết */}
                      <div className="absolute inset-0 bg-gradient-to-br from-emerald-950 via-emerald-900 to-teal-900"></div>
                      <div className="absolute -top-10 -right-10 w-40 h-40 rounded-full bg-emerald-400/20 blur-2xl"></div>
                      <div className="absolute -bottom-12 -left-12 w-44 h-44 rounded-full bg-teal-300/20 blur-2xl"></div>

                      <div className="relative p-5 sm:p-6 flex flex-col items-center gap-4 text-center">
                        <div className="flex items-center gap-2 text-emerald-100/90">
                          <i className="fa-solid fa-building-columns text-sm"></i>
                          <span className="text-[11px] font-bold uppercase tracking-[0.2em]">
                            Vietcombank • Chuyển khoản QR
                          </span>
                        </div>

                        <div className="bg-white p-3 rounded-2xl shadow-lg ring-4 ring-white/20">
                          <QRCodeSVG
                            value={buildVietQR(SHOP_BANK_BIN, SHOP_BANK_ACC, totalVND, 'BOKO THANH TOAN')}
                            size={180}
                            level="M"
                          />
                        </div>

                        <div>
                          <p className="text-[11px] uppercase tracking-widest text-emerald-200/80 font-semibold">
                            Số tiền cần chuyển
                          </p>
                          <p className="font-display font-bold text-3xl text-white tracking-tight">
                            {totalVND.toLocaleString('vi-VN')} ₫
                          </p>
                        </div>

                        <div className="w-full bg-white/10 backdrop-blur-sm border border-white/15 rounded-xl px-4 py-3 text-left space-y-2">
                          <div className="flex items-center justify-between gap-3">
                            <div className="min-w-0">
                              <p className="text-[10px] uppercase tracking-wider text-emerald-200/70 font-semibold">Số tài khoản</p>
                              <p className="font-mono font-bold text-white text-sm tracking-wider">8899 2026 8888</p>
                            </div>
                            <button
                              type="button"
                              onClick={handleCopyAccount}
                              className="shrink-0 px-3 py-1.5 rounded-lg bg-white/15 hover:bg-white/25 text-white text-[11px] font-bold flex items-center gap-1.5 transition-colors cursor-pointer"
                            >
                              <i className="fa-solid fa-copy text-xs"></i>
                              <span>{copiedBank ? 'Đã chép!' : 'Chép STK'}</span>
                            </button>
                          </div>
                          <p className="text-[11px] text-emerald-100/80 border-t border-white/10 pt-2">
                            Chủ TK: <strong className="text-white">BIBLIOTHECA STILLE CO., LTD</strong>
                            <span className="mx-1">•</span>
                            Nội dung: <strong className="text-white font-mono">BOKO THANH TOAN</strong>
                          </p>
                        </div>

                        <p className="text-[11px] text-emerald-100/70 font-body">
                          Mở app ngân hàng bất kỳ → quét mã → kiểm tra đúng số tiền rồi xác nhận.
                        </p>
                      </div>
                    </div>
                      <div className="pt-2 border-t border-slate-200 w-full">
                        <button
                          type="button"
                          onClick={() => handleOpenSettingsTab('payments')}
                          className="text-xs text-blue-600 hover:underline font-semibold flex items-center justify-center gap-1.5 mx-auto cursor-pointer"
                        >
                          <i className="fa-solid fa-gear text-xs"></i>
                          <span>Quản lý tài khoản ngân hàng trong Cài đặt</span>
                        </button>
                      </div>
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* Action Navigation */}
          <div className="flex flex-col-reverse sm:flex-row justify-between items-center pt-8 border-t border-slate-200 mt-10 gap-6">
            <button
              type="button"
              onClick={onReturnToCart}
              className="font-label-caps text-xs text-slate-500 hover:text-slate-900 transition-colors flex items-center gap-2 uppercase tracking-wider font-semibold cursor-pointer"
            >
              <i className="fa-solid fa-arrow-left text-sm"></i>
              <span>Return to Cart</span>
            </button>

            <button
              type="button"
              onClick={handleSubmitOrder}
              disabled={isSubmittingMomo || isSubmittingVnpay || isSubmittingPaypal || isSubmittingAtm}
              className={`text-white font-label-caps text-xs py-4 px-10 rounded-lg w-full sm:w-auto uppercase tracking-widest font-bold shadow-md transition-all cursor-pointer flex items-center justify-center gap-2 ${
                formData.paymentMethod === 'ewallet' && formData.ewalletType === 'momo'
                  ? 'bg-[#a50064] hover:bg-[#860051] shadow-pink-200'
                  : formData.paymentMethod === 'atm'
                  ? 'bg-teal-700 hover:bg-teal-800 shadow-teal-200'
                  : 'bg-blue-600 hover:bg-blue-700 shadow-blue-200'
              } disabled:opacity-60`}
            >
              {isSubmittingMomo ? (
                <>
                  <span className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"></span>
                  <span>ĐANG KẾT NỐI MOMO...</span>
                </>
              ) : isSubmittingVnpay ? (
                <>
                  <span className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"></span>
                  <span>ĐANG KẾT NỐI VNPAY...</span>
                </>
              ) : isSubmittingPaypal ? (
                <>
                  <span className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"></span>
                  <span>ĐANG KẾT NỐI PAYPAL...</span>
                </>
              ) : isSubmittingAtm ? (
                <>
                  <span className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"></span>
                  <span>ĐANG KẾT NỐI CỔNG NCB...</span>
                </>
              ) : formData.paymentMethod === 'paypal' ? (
                'ĐẶT HÀNG & THANH TOÁN PAYPAL'
              ) : formData.paymentMethod === 'ewallet' && formData.ewalletType === 'vnpay' ? (
                'ĐẶT HÀNG & THANH TOÁN VNPAY'
              ) : formData.paymentMethod === 'ewallet' && formData.ewalletType === 'momo' ? (
                'ĐẶT HÀNG & THANH TOÁN MOMO'
              ) : formData.paymentMethod === 'atm' ? (
                'XÁC THỰC OTP & THANH TOÁN THẺ NCB'
              ) : (
                'CONTINUE'
              )}
            </button>
          </div>
        </section>

        {/* Right Column: Order Summary (Sleek Panel) */}
        <aside className="lg:col-span-5 bg-white p-6 sm:p-8 rounded-2xl border border-slate-200 shadow-sm relative overflow-hidden">
          <h2 className="font-display font-bold text-2xl text-slate-900 mb-6">
            Your Order
          </h2>

          {/* Book Items List */}
          <div className="space-y-5 pb-6 border-b border-slate-200">
            {displayItems.map((item) => (
              <div key={item.book.id} className="flex items-center gap-4">
                <div className="w-16 shrink-0 relative">
                  <div className="absolute -top-2 -right-2 bg-blue-600 text-white font-label-caps text-[10px] w-5 h-5 flex items-center justify-center rounded-full font-bold z-20 shadow-xs">
                    {item.quantity}
                  </div>
                  <div className="w-full h-20 bg-slate-100 rounded-md overflow-hidden book-spine-shadow border border-slate-200">
                    {item.book.coverUrl ? (
                      <img
                        src={item.book.coverUrl}
                        alt={item.book.title}
                        className="w-full h-full object-cover"
                      />
                    ) : (
                      <div
                        style={{ backgroundColor: item.book.bgColor || '#334155' }}
                        className="w-full h-full flex items-center justify-center p-1 text-[8px] text-white font-bold text-center"
                      >
                        {item.book.title}
                      </div>
                    )}
                  </div>
                </div>

                <div className="flex-grow flex justify-between items-center min-w-0">
                  <div className="pr-2 min-w-0">
                    <div className="flex items-center gap-1 mb-0.5">
                      <span className="text-[10px] font-bold text-blue-700 bg-blue-50 px-1.5 py-0.2 rounded border border-blue-200 truncate max-w-[150px] flex items-center gap-1">
                        <i className="fa-solid fa-circle-check text-blue-500 text-[10px]"></i>
                        <span>{item.book.brandName || 'Chính Hãng'}</span>
                      </span>
                    </div>
                    <h3 className="font-display font-bold text-sm text-slate-900 leading-tight truncate">
                      {item.book.title}
                    </h3>
                    <p className="font-body text-xs text-slate-500 mt-0.5 truncate">
                      {item.book.author} • {item.book.editionType || 'Bìa Mềm'}
                    </p>
                  </div>
                  <span className="font-body text-sm text-slate-900 font-bold shrink-0">
                    {formatPrice(item.book.priceEUR * item.quantity, item.book.priceVND * item.quantity)}
                  </span>
                </div>
              </div>
            ))}
          </div>

          {/* Discount Code */}
          <form onSubmit={handleApplyDiscount} className="py-5 border-b border-slate-200 space-y-2">
            <div className="flex gap-3">
              <input
                type="text"
                placeholder="Discount Code"
                value={discountInput}
                onChange={(e) => setDiscountInput(e.target.value)}
                className="paper-input flex-grow font-body text-xs text-slate-900 placeholder:text-slate-400 bg-slate-50 border-slate-200"
              />
              <button
                type="submit"
                className="bg-slate-900 text-white hover:bg-slate-800 transition-colors font-label-caps text-[11px] py-2 px-5 rounded-lg uppercase tracking-widest font-bold cursor-pointer"
              >
                APPLY
              </button>
            </div>
            {discountMessage && (
              <p className={`text-xs font-semibold ${discountMessage.isError ? 'text-red-600' : 'text-green-600'}`}>
                {discountMessage.text}
              </p>
            )}
          </form>

          {/* Totals */}
          <div className="pt-5 space-y-3 font-body text-sm text-slate-600">
            <div className="flex justify-between">
              <span>Subtotal</span>
              <span className="font-semibold text-slate-900">
                {formatPrice(subtotalEUR, subtotalVND)}
              </span>
            </div>

            {discountRate > 0 && (
              <div className="flex justify-between text-red-600">
                <span>Discount ({discountRate * 100}%)</span>
                <span>-{formatPrice(discountEUR, discountVND)}</span>
              </div>
            )}

            <div className="flex justify-between">
              <span>VAT (5%)</span>
              <span className="font-semibold text-slate-900">{formatPrice(vatEUR, vatVND)}</span>
            </div>

            <div className="flex justify-between">
              <span>Shipping</span>
              <span className="font-semibold text-slate-900">
                {isFreeShip ? 'FREE' : formatPrice(shippingEUR, shippingVND)}
              </span>
            </div>
          </div>

          <div className="pt-5 mt-5 border-t border-slate-200 flex justify-between items-end">
            <span className="font-display font-bold text-xl text-slate-900">Total</span>
            <span className="font-display font-bold text-3xl text-slate-900">
              {formatPrice(totalEUR, totalVND)}
            </span>
          </div>
        </aside>
      </main>

      {/* NCB Smart OTP Modal (1-Chạm cho Thẻ ATM Nội Địa) */}
      <NcbOtpModal
        isOpen={showNcbOtpModal}
        onClose={() => setShowNcbOtpModal(false)}
        amountVND={totalVND}
        cardNumber={savedAtm[0]?.accountNumber || '9704 •••• •••• 1432'}
        cardHolder={savedAtm[0]?.accountHolder || formData.firstName || 'NGUYEN VAN A'}
        bankName={savedAtm[0]?.bankName || 'NCB (Ngân hàng Quốc Dân - Sandbox)'}
        shippingAddress={buildShippingAddress() || 'Địa chỉ nhận sách Boko'}
        phone={formData.telephone}
        email={formData.email}
        customerName={`${formData.firstName} ${formData.lastName}`.trim()}
        couponCode={appliedDiscountCode}
        items={displayItems.map((item) => ({
          book_id: typeof item.book.id === 'number' ? item.book.id : 1,
          title: item.book.title,
          price: item.book.priceVND,
          quantity: item.quantity,
        }))}
        onSuccess={handleAtmPaymentSuccess}
      />
    </div>
  );
};

