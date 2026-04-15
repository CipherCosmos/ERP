import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import '../services/api.dart';
import '../models/models.dart';
import 'login.dart';

class HomeScreen extends StatefulWidget {
  @override
  _HomeScreenState createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  int _currentIndex = 0;
  Map<String, dynamic>? _dashboard;
  List<dynamic>? _lowStock;

  @override
  void initState() {
    super.initState();
    _loadData();
  }

  Future<void> _loadData() async {
    try {
      final dashboard = await ApiService.getDashboard();
      final lowStock = await ApiService.getLowStock();
      setState(() {
        _dashboard = dashboard;
        _lowStock = lowStock;
      });
    } catch (e) {
      print('Error loading data: $e');
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: Text('GBOS'),
        actions: [
          IconButton(
            icon: Icon(Icons.refresh),
            onPressed: _loadData,
          ),
          IconButton(
            icon: Icon(Icons.logout),
            onPressed: () async {
              await ApiService.logout();
              if (mounted) {
                Navigator.of(context).pushReplacement(
                  MaterialPageRoute(builder: (_) => LoginScreen()),
                );
              }
            },
          ),
        ],
      ),
      body: _buildBody(),
      bottomNavigationBar: BottomNavigationBar(
        currentIndex: _currentIndex,
        onTap: (i) => setState(() => _currentIndex = i),
        items: [
          BottomNavigationBarItem(icon: Icon(Icons.dashboard), label: 'Dashboard'),
          BottomNavigationBarItem(icon: Icon(Icons.inventory), label: 'Stock'),
          BottomNavigationBarItem(icon: Icon(Icons.people), label: 'Customers'),
          BottomNavigationBarItem(icon: Icon(Icons.receipt), label: 'Invoices'),
        ],
      ),
    );
  }

  Widget _buildBody() {
    switch (_currentIndex) {
      case 0:
        return _buildDashboard();
      case 1:
        return _buildStock();
      case 2:
        return _buildCustomers();
      case 3:
        return _buildInvoices();
      default:
        return _buildDashboard();
    }
  }

  Widget _buildDashboard() {
    if (_dashboard == null) return Center(child: CircularProgressIndicator());

    return RefreshIndicator(
      onRefresh: _loadData,
      child: ListView(
        padding: EdgeInsets.all(16),
        children: [
          _buildStatCard('Revenue', '₹${(_dashboard!['revenue'] ?? 0).toStringAsFixed(2)}', Colors.blue),
          _buildStatCard('GST Collected', '₹${(_dashboard!['gst_collected'] ?? 0).toStringAsFixed(2)}', Colors.green),
          _buildStatCard('Invoices', '${_dashboard!['invoice_count'] ?? 0}', Colors.orange),
          _buildStatCard('Avg Bill', '₹${(_dashboard!['avg_bill_value'] ?? 0).toStringAsFixed(2)}', Colors.purple),
          if ((_lowStock?.length ?? 0) > 0) ...[
            SizedBox(height: 16),
            Card(
              color: Colors.orange[50],
              child: ListTile(
                leading: Icon(Icons.warning, color: Colors.orange),
                title: Text('${_lowStock!.length} Low Stock Items'),
                trailing: Icon(Icons.arrow_forward_ios, size: 16),
                onTap: () => setState(() => _currentIndex = 1),
              ),
            ),
          ],
          if ((_dashboard!['credit_outstanding'] ?? 0) > 0) ...[
            SizedBox(height: 8),
            Card(
              color: Colors.red[50],
              child: ListTile(
                leading: Icon(Icons.money_off, color: Colors.red),
                title: Text('Credit: ₹${(_dashboard!['credit_outstanding']).toStringAsFixed(2)}'),
                trailing: Icon(Icons.arrow_forward_ios, size: 16),
              ),
            ),
          ],
        ],
      ),
    );
  }

  Widget _buildStatCard(String label, String value, Color color) {
    return Card(
      child: Padding(
        padding: EdgeInsets.all(16),
        child: Column(
          children: [
            Text(label, style: TextStyle(color: Colors.grey[600])),
            SizedBox(height: 8),
            Text(value, style: TextStyle(fontSize: 24, fontWeight: FontWeight.bold, color: color)),
          ],
        ),
      ),
    );
  }

  Widget _buildStock() {
    return StockTab();
  }

  Widget _buildCustomers() {
    return CustomersTab();
  }

  Widget _buildInvoices() {
    return InvoicesTab();
  }
}

// Stock Tab
class StockTab extends StatefulWidget {
  @override
  _StockTabState createState() => _StockTabState();
}

class _StockTabState extends State<StockTab> {
  late Future<List<Stock>> _stockFuture;

  @override
  void initState() {
    super.initState();
    _stockFuture = ApiService.getStock();
  }

  @override
  Widget build(BuildContext context) {
    return RefreshIndicator(
      onRefresh: () async {
        setState(() {
          _stockFuture = ApiService.getStock();
        });
      },
      child: FutureBuilder<List<Stock>>(
        future: _stockFuture,
        builder: (context, snapshot) {
          if (snapshot.connectionState == ConnectionState.waiting) {
            return Center(child: CircularProgressIndicator());
          }
          if (snapshot.hasError) {
            return Center(child: Text('Error: ${snapshot.error}'));
          }
          final stock = snapshot.data ?? [];
          if (stock.isEmpty) {
            return Center(child: Text('No stock available'));
          }
          return ListView.builder(
            itemCount: stock.length,
            itemBuilder: (context, index) {
              final item = stock[index];
              return Card(
                margin: EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                child: ListTile(
                  title: Text('Product ID: ${item.productId}'),
                  subtitle: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      SizedBox(height: 4),
                      Text('Batch: ${item.batchNo}'),
                      Text('Qty: ${item.quantity} (Reserved: ${item.reservedQty})'),
                      if (item.expiryDate != null) Text('Expiry: ${item.expiryDate}'),
                    ],
                  ),
                  trailing: Text('₹${item.costPrice.toStringAsFixed(2)}'),
                ),
              );
            },
          );
        },
      ),
    );
  }
}

// Customers Tab
class CustomersTab extends StatefulWidget {
  @override
  _CustomersTabState createState() => _CustomersTabState();
}

class _CustomersTabState extends State<CustomersTab> {
  late Future<List<Customer>> _customersFuture;
  TextEditingController _searchController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _customersFuture = ApiService.getCustomers();
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        Padding(
          padding: EdgeInsets.all(8),
          child: TextField(
            controller: _searchController,
            decoration: InputDecoration(
              hintText: 'Search customers...',
              prefixIcon: Icon(Icons.search),
              border: OutlineInputBorder(borderRadius: BorderRadius.circular(8)),
            ),
            onChanged: (query) {
              setState(() {
                _customersFuture = ApiService.getCustomers(search: query);
              });
            },
          ),
        ),
        Expanded(
          child: FutureBuilder<List<Customer>>(
            future: _customersFuture,
            builder: (context, snapshot) {
              if (snapshot.connectionState == ConnectionState.waiting) {
                return Center(child: CircularProgressIndicator());
              }
              if (snapshot.hasError) {
                return Center(child: Text('Error: ${snapshot.error}'));
              }
              final customers = snapshot.data ?? [];
              if (customers.isEmpty) {
                return Center(child: Text('No customers found'));
              }
              return ListView.builder(
                itemCount: customers.length,
                itemBuilder: (context, index) {
                  final customer = customers[index];
                  return Card(
                    margin: EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                    child: ListTile(
                      title: Text(customer.name),
                      subtitle: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          SizedBox(height: 4),
                          Text('Phone: ${customer.phone}'),
                          Text('Outstanding: ₹${customer.outstanding.toStringAsFixed(2)}'),
                          Text('Credit Limit: ₹${customer.creditLimit.toStringAsFixed(2)}'),
                        ],
                      ),
                      isThreeLine: true,
                    ),
                  );
                },
              );
            },
          ),
        ),
      ],
    );
  }

  @override
  void dispose() {
    _searchController.dispose();
    super.dispose();
  }
}

// Invoices Tab
class InvoicesTab extends StatefulWidget {
  @override
  _InvoicesTabState createState() => _InvoicesTabState();
}

class _InvoicesTabState extends State<InvoicesTab> {
  late Future<List<Invoice>> _invoicesFuture;
  DateTime? _startDate;
  DateTime? _endDate;

  @override
  void initState() {
    super.initState();
    _invoicesFuture = ApiService.getInvoices();
  }

  Future<void> _pickDateRange() async {
    final picked = await showDateRangePicker(
      context: context,
      firstDate: DateTime(2020),
      lastDate: DateTime.now(),
    );
    if (picked != null) {
      setState(() {
        _startDate = picked.start;
        _endDate = picked.end;
        _invoicesFuture = ApiService.getInvoices(
          startDate: DateFormat('yyyy-MM-dd').format(_startDate!),
          endDate: DateFormat('yyyy-MM-dd').format(_endDate!),
        );
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        Padding(
          padding: EdgeInsets.all(8),
          child: Row(
            children: [
              Expanded(
                child: Text(
                  _startDate != null
                      ? '${DateFormat('dd/MM/yyyy').format(_startDate!)} - ${DateFormat('dd/MM/yyyy').format(_endDate!)}'
                      : 'Select date range',
                  style: TextStyle(fontSize: 12),
                ),
              ),
              ElevatedButton(
                onPressed: _pickDateRange,
                child: Text('Filter'),
              ),
            ],
          ),
        ),
        Expanded(
          child: FutureBuilder<List<Invoice>>(
            future: _invoicesFuture,
            builder: (context, snapshot) {
              if (snapshot.connectionState == ConnectionState.waiting) {
                return Center(child: CircularProgressIndicator());
              }
              if (snapshot.hasError) {
                return Center(child: Text('Error: ${snapshot.error}'));
              }
              final invoices = snapshot.data ?? [];
              if (invoices.isEmpty) {
                return Center(child: Text('No invoices found'));
              }
              return ListView.builder(
                itemCount: invoices.length,
                itemBuilder: (context, index) {
                  final invoice = invoices[index];
                  return Card(
                    margin: EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                    child: ListTile(
                      title: Text('${invoice.invoiceNo}'),
                      subtitle: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          SizedBox(height: 4),
                          Text('Date: ${DateFormat('dd/MM/yyyy').format(invoice.invoiceDate)}'),
                          Text('Payment: ${invoice.paymentMode}'),
                          Text('Status: ${invoice.status}'),
                        ],
                      ),
                      trailing: Text('₹${invoice.total.toStringAsFixed(2)}',
                          style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
                      isThreeLine: true,
                    ),
                  );
                },
              );
            },
          ),
        ),
      ],
    );
  }
}