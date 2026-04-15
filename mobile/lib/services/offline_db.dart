import 'package:sqflite/sqflite.dart';
import 'package:path/path.dart';
import '../models/models.dart';

class OfflineDb {
  static final OfflineDb _instance = OfflineDb._internal();
  static Database? _database;

  factory OfflineDb() {
    return _instance;
  }

  OfflineDb._internal();

  Future<Database> get database async {
    if (_database != null) return _database!;
    _database = await _initDb();
    return _database!;
  }

  Future<Database> _initDb() async {
    final dbPath = await getDatabasesPath();
    final path = join(dbPath, 'gbos.db');
    return await openDatabase(
      path,
      version: 1,
      onCreate: _onCreate,
    );
  }

  Future<void> _onCreate(Database db, int version) async {
    // Products table
    await db.execute('''
      CREATE TABLE products (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        name_hi TEXT,
        name_kn TEXT,
        category_id TEXT,
        hsn_code TEXT,
        barcode TEXT,
        uom_id TEXT,
        base_unit_qty REAL,
        gst_rate REAL,
        is_free INTEGER,
        is_active INTEGER,
        allow_decimal INTEGER,
        min_stock REAL,
        sell_price REAL,
        mrp REAL,
        sync_status TEXT DEFAULT 'synced'
      )
    ''');

    // Customers table
    await db.execute('''
      CREATE TABLE customers (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        name_hi TEXT,
        phone TEXT,
        address TEXT,
        gstin TEXT,
        credit_limit REAL,
        outstanding REAL,
        is_active INTEGER,
        sync_status TEXT DEFAULT 'synced'
      )
    ''');

    // Stock table
    await db.execute('''
      CREATE TABLE stock (
        id TEXT PRIMARY KEY,
        product_id TEXT,
        store_id TEXT,
        batch_no TEXT,
        quantity REAL,
        reserved_qty REAL,
        expiry_date TEXT,
        cost_price REAL,
        sync_status TEXT DEFAULT 'synced'
      )
    ''');

    // Invoices table
    await db.execute('''
      CREATE TABLE invoices (
        id TEXT PRIMARY KEY,
        invoice_no TEXT,
        store_id TEXT,
        user_id TEXT,
        customer_id TEXT,
        invoice_date TEXT,
        sub_total REAL,
        cgst REAL,
        sgst REAL,
        igst REAL,
        discount REAL,
        round_off REAL,
        total REAL,
        status TEXT,
        payment_mode TEXT,
        sync_status TEXT DEFAULT 'pending'
      )
    ''');

    // Invoice items table
    await db.execute('''
      CREATE TABLE invoice_items (
        id TEXT PRIMARY KEY,
        invoice_id TEXT,
        product_id TEXT,
        batch_no TEXT,
        quantity REAL,
        rate REAL,
        discount REAL,
        cgst REAL,
        sgst REAL,
        igst REAL,
        total REAL,
        sync_status TEXT DEFAULT 'pending'
      )
    ''');

    // Last sync timestamp
    await db.execute('''
      CREATE TABLE sync_metadata (
        key TEXT PRIMARY KEY,
        value TEXT
      )
    ''');
  }

  // Products
  Future<void> insertProducts(List<Product> products) async {
    final db = await database;
    for (var product in products) {
      await db.insert(
        'products',
        {
          'id': product.id,
          'name': product.name,
          'name_hi': product.nameHi,
          'name_kn': product.nameKn,
          'category_id': product.categoryId,
          'hsn_code': product.hsnCode,
          'barcode': product.barcode,
          'uom_id': product.uomId,
          'base_unit_qty': product.baseUnitQty,
          'gst_rate': product.gstRate,
          'is_free': product.isFree ? 1 : 0,
          'is_active': product.isActive ? 1 : 0,
          'allow_decimal': product.allowDecimal ? 1 : 0,
          'min_stock': product.minStock,
          'sell_price': product.sellPrice,
          'mrp': product.mrp,
        },
        conflictAlgorithm: ConflictAlgorithm.replace,
      );
    }
  }

  Future<List<Product>> getProducts() async {
    final db = await database;
    final maps = await db.query('products');
    return List.generate(
      maps.length,
      (i) => Product(
        id: maps[i]['id'] as String,
        name: maps[i]['name'] as String,
        nameHi: maps[i]['name_hi'] as String? ?? '',
        nameKn: maps[i]['name_kn'] as String? ?? '',
        categoryId: maps[i]['category_id'] as String? ?? '',
        hsnCode: maps[i]['hsn_code'] as String? ?? '',
        barcode: maps[i]['barcode'] as String? ?? '',
        uomId: maps[i]['uom_id'] as String? ?? '',
        baseUnitQty: (maps[i]['base_unit_qty'] as num?)?.toDouble() ?? 1,
        gstRate: (maps[i]['gst_rate'] as num?)?.toDouble() ?? 0,
        isFree: (maps[i]['is_free'] as int?) == 1,
        isActive: (maps[i]['is_active'] as int?) == 1,
        allowDecimal: (maps[i]['allow_decimal'] as int?) == 1,
        minStock: (maps[i]['min_stock'] as num?)?.toDouble() ?? 0,
        sellPrice: (maps[i]['sell_price'] as num?)?.toDouble() ?? 0,
        mrp: (maps[i]['mrp'] as num?)?.toDouble() ?? 0,
      ),
    );
  }

  // Customers
  Future<void> insertCustomers(List<Customer> customers) async {
    final db = await database;
    for (var customer in customers) {
      await db.insert(
        'customers',
        {
          'id': customer.id,
          'name': customer.name,
          'name_hi': customer.nameHi,
          'phone': customer.phone,
          'address': customer.address,
          'gstin': customer.gstin,
          'credit_limit': customer.creditLimit,
          'outstanding': customer.outstanding,
          'is_active': customer.isActive ? 1 : 0,
        },
        conflictAlgorithm: ConflictAlgorithm.replace,
      );
    }
  }

  Future<List<Customer>> getCustomers() async {
    final db = await database;
    final maps = await db.query('customers');
    return List.generate(
      maps.length,
      (i) => Customer(
        id: maps[i]['id'] as String,
        name: maps[i]['name'] as String,
        nameHi: maps[i]['name_hi'] as String? ?? '',
        phone: maps[i]['phone'] as String? ?? '',
        address: maps[i]['address'] as String? ?? '',
        gstin: maps[i]['gstin'] as String? ?? '',
        creditLimit: (maps[i]['credit_limit'] as num?)?.toDouble() ?? 0,
        outstanding: (maps[i]['outstanding'] as num?)?.toDouble() ?? 0,
        isActive: (maps[i]['is_active'] as int?) == 1,
      ),
    );
  }

  // Stock
  Future<void> insertStock(List<Stock> stocks) async {
    final db = await database;
    for (var stock in stocks) {
      await db.insert(
        'stock',
        {
          'id': stock.id,
          'product_id': stock.productId,
          'store_id': stock.storeId,
          'batch_no': stock.batchNo,
          'quantity': stock.quantity,
          'reserved_qty': stock.reservedQty,
          'expiry_date': stock.expiryDate,
          'cost_price': stock.costPrice,
        },
        conflictAlgorithm: ConflictAlgorithm.replace,
      );
    }
  }

  Future<List<Stock>> getStock() async {
    final db = await database;
    final maps = await db.query('stock');
    return List.generate(
      maps.length,
      (i) => Stock(
        id: maps[i]['id'] as String,
        productId: maps[i]['product_id'] as String,
        storeId: maps[i]['store_id'] as String,
        batchNo: maps[i]['batch_no'] as String? ?? '',
        quantity: (maps[i]['quantity'] as num?)?.toDouble() ?? 0,
        reservedQty: (maps[i]['reserved_qty'] as num?)?.toDouble() ?? 0,
        expiryDate: maps[i]['expiry_date'] as String?,
        costPrice: (maps[i]['cost_price'] as num?)?.toDouble() ?? 0,
      ),
    );
  }

  // Invoices
  Future<void> insertInvoice(Invoice invoice, List<Map<String, dynamic>> items) async {
    final db = await database;
    await db.insert(
      'invoices',
      {
        'id': invoice.id,
        'invoice_no': invoice.invoiceNo,
        'store_id': invoice.storeId,
        'user_id': invoice.userId,
        'customer_id': invoice.customerId,
        'invoice_date': invoice.invoiceDate.toIso8601String(),
        'sub_total': invoice.subTotal,
        'cgst': invoice.cgst,
        'sgst': invoice.sgst,
        'igst': invoice.igst,
        'discount': invoice.discount,
        'round_off': invoice.roundOff,
        'total': invoice.total,
        'status': invoice.status,
        'payment_mode': invoice.paymentMode,
      },
      conflictAlgorithm: ConflictAlgorithm.replace,
    );
    for (var item in items) {
      await db.insert(
        'invoice_items',
        item,
        conflictAlgorithm: ConflictAlgorithm.replace,
      );
    }
  }

  Future<List<Invoice>> getInvoices() async {
    final db = await database;
    final maps = await db.query('invoices');
    return List.generate(
      maps.length,
      (i) => Invoice(
        id: maps[i]['id'] as String,
        invoiceNo: maps[i]['invoice_no'] as String,
        storeId: maps[i]['store_id'] as String,
        userId: maps[i]['user_id'] as String,
        customerId: maps[i]['customer_id'] as String?,
        invoiceDate: DateTime.parse(maps[i]['invoice_date'] as String),
        subTotal: (maps[i]['sub_total'] as num?)?.toDouble() ?? 0,
        cgst: (maps[i]['cgst'] as num?)?.toDouble() ?? 0,
        sgst: (maps[i]['sgst'] as num?)?.toDouble() ?? 0,
        igst: (maps[i]['igst'] as num?)?.toDouble() ?? 0,
        discount: (maps[i]['discount'] as num?)?.toDouble() ?? 0,
        roundOff: (maps[i]['round_off'] as num?)?.toDouble() ?? 0,
        total: (maps[i]['total'] as num?)?.toDouble() ?? 0,
        status: maps[i]['status'] as String? ?? 'completed',
        paymentMode: maps[i]['payment_mode'] as String? ?? 'cash',
      ),
    );
  }

  // Sync metadata
  Future<void> setSyncTime(String key, DateTime time) async {
    final db = await database;
    await db.insert(
      'sync_metadata',
      {
        'key': key,
        'value': time.toIso8601String(),
      },
      conflictAlgorithm: ConflictAlgorithm.replace,
    );
  }

  Future<DateTime?> getSyncTime(String key) async {
    final db = await database;
    final result = await db.query(
      'sync_metadata',
      where: 'key = ?',
      whereArgs: [key],
    );
    if (result.isNotEmpty) {
      return DateTime.parse(result.first['value'] as String);
    }
    return null;
  }

  // Clear all data
  Future<void> clearAll() async {
    final db = await database;
    await db.delete('invoice_items');
    await db.delete('invoices');
    await db.delete('stock');
    await db.delete('customers');
    await db.delete('products');
  }
}
