class Product {
  final String id;
  final String name;
  final String nameHi;
  final String nameKn;
  final String categoryId;
  final String hsnCode;
  final String barcode;
  final String uomId;
  final double baseUnitQty;
  final double gstRate;
  final bool isFree;
  final bool isActive;
  final bool allowDecimal;
  final double minStock;
  final double sellPrice;
  final double mrp;

  Product({
    required this.id,
    required this.name,
    this.nameHi = '',
    this.nameKn = '',
    required this.categoryId,
    this.hsnCode = '',
    this.barcode = '',
    required this.uomId,
    this.baseUnitQty = 1,
    this.gstRate = 0,
    this.isFree = false,
    this.isActive = true,
    this.allowDecimal = false,
    this.minStock = 0,
    this.sellPrice = 0,
    this.mrp = 0,
  });

  factory Product.fromJson(Map<String, dynamic> json) {
    return Product(
      id: json['id'] ?? '',
      name: json['name'] ?? '',
      nameHi: json['name_hi'] ?? '',
      nameKn: json['name_kn'] ?? '',
      categoryId: json['category_id'] ?? '',
      hsnCode: json['hsn_code'] ?? '',
      barcode: json['barcode'] ?? '',
      uomId: json['uom_id'] ?? '',
      baseUnitQty: (json['base_unit_qty'] ?? 1).toDouble(),
      gstRate: (json['gst_rate'] ?? 0).toDouble(),
      isFree: json['is_free'] ?? false,
      isActive: json['is_active'] ?? true,
      allowDecimal: json['allow_decimal'] ?? false,
      minStock: (json['min_stock'] ?? 0).toDouble(),
      sellPrice: (json['sell_price'] ?? 0).toDouble(),
      mrp: (json['mrp'] ?? 0).toDouble(),
    );
  }

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'name_hi': nameHi,
    'name_kn': nameKn,
    'category_id': categoryId,
    'hsn_code': hsnCode,
    'barcode': barcode,
    'uom_id': uomId,
    'base_unit_qty': baseUnitQty,
    'gst_rate': gstRate,
    'is_free': isFree,
    'is_active': isActive,
    'allow_decimal': allowDecimal,
    'min_stock': minStock,
  };
}

class Stock {
  final String id;
  final String productId;
  final String storeId;
  final String batchNo;
  final double quantity;
  final double reservedQty;
  final String? expiryDate;
  final double costPrice;

  Stock({
    required this.id,
    required this.productId,
    required this.storeId,
    this.batchNo = '',
    this.quantity = 0,
    this.reservedQty = 0,
    this.expiryDate,
    this.costPrice = 0,
  });

  factory Stock.fromJson(Map<String, dynamic> json) {
    return Stock(
      id: json['id'] ?? '',
      productId: json['product_id'] ?? '',
      storeId: json['store_id'] ?? '',
      batchNo: json['batch_no'] ?? '',
      quantity: (json['quantity'] ?? 0).toDouble(),
      reservedQty: (json['reserved_qty'] ?? 0).toDouble(),
      expiryDate: json['expiry_date'],
      costPrice: (json['cost_price'] ?? 0).toDouble(),
    );
  }
}

class Customer {
  final String id;
  final String name;
  final String nameHi;
  final String phone;
  final String address;
  final String gstin;
  final double creditLimit;
  final double outstanding;
  final bool isActive;

  Customer({
    required this.id,
    required this.name,
    this.nameHi = '',
    this.phone = '',
    this.address = '',
    this.gstin = '',
    this.creditLimit = 0,
    this.outstanding = 0,
    this.isActive = true,
  });

  factory Customer.fromJson(Map<String, dynamic> json) {
    return Customer(
      id: json['id'] ?? '',
      name: json['name'] ?? '',
      nameHi: json['name_hi'] ?? '',
      phone: json['phone'] ?? '',
      address: json['address'] ?? '',
      gstin: json['gstin'] ?? '',
      creditLimit: (json['credit_limit'] ?? 0).toDouble(),
      outstanding: (json['outstanding'] ?? 0).toDouble(),
      isActive: json['is_active'] ?? true,
    );
  }
}

class Invoice {
  final String id;
  final String invoiceNo;
  final String storeId;
  final String userId;
  final String? customerId;
  final DateTime invoiceDate;
  final double subTotal;
  final double cgst;
  final double sgst;
  final double igst;
  final double discount;
  final double roundOff;
  final double total;
  final String status;
  final String paymentMode;

  Invoice({
    required this.id,
    required this.invoiceNo,
    required this.storeId,
    required this.userId,
    this.customerId,
    required this.invoiceDate,
    this.subTotal = 0,
    this.cgst = 0,
    this.sgst = 0,
    this.igst = 0,
    this.discount = 0,
    this.roundOff = 0,
    required this.total,
    this.status = 'completed',
    this.paymentMode = 'cash',
  });

  factory Invoice.fromJson(Map<String, dynamic> json) {
    return Invoice(
      id: json['id'] ?? '',
      invoiceNo: json['invoice_no'] ?? '',
      storeId: json['store_id'] ?? '',
      userId: json['user_id'] ?? '',
      customerId: json['customer_id'],
      invoiceDate: DateTime.parse(json['invoice_date']),
      subTotal: (json['sub_total'] ?? 0).toDouble(),
      cgst: (json['cgst'] ?? 0).toDouble(),
      sgst: (json['sgst'] ?? 0).toDouble(),
      igst: (json['igst'] ?? 0).toDouble(),
      discount: (json['discount'] ?? 0).toDouble(),
      roundOff: (json['round_off'] ?? 0).toDouble(),
      total: (json['total'] ?? 0).toDouble(),
      status: json['status'] ?? 'completed',
      paymentMode: json['payment_mode'] ?? 'cash',
    );
  }
}