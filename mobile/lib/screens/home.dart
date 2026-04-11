import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../services/api.dart';
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
    return Center(child: Text('Stock Management', style: TextStyle(fontSize: 18)));
  }

  Widget _buildCustomers() {
    return Center(child: Text('Customer Management', style: TextStyle(fontSize: 18)));
  }

  Widget _buildInvoices() {
    return Center(child: Text('Invoice History', style: TextStyle(fontSize: 18)));
  }
}