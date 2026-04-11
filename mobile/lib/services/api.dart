import 'dart:convert';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../models/models.dart';

class ApiService {
  static String baseUrl = 'http://10.0.2.2:8080/api/v1';
  
  static Future<Map<String, String>> _headers() async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('token');
    final storeId = prefs.getString('storeId');
    return {
      'Content-Type': 'application/json',
      if (token != null) 'Authorization': 'Bearer $token',
      if (storeId != null) 'X-Store-ID': storeId,
    };
  }

  static Future<Map<String, dynamic>> login(String username, String password) async {
    final response = await http.post(
      Uri.parse('$baseUrl/auth/login'),
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'username': username, 'password': password}),
    );
    if (response.statusCode == 200) {
      final data = jsonDecode(response.body);
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('token', data['token']);
      await prefs.setString('storeId', data['user']['store_id']);
      return data;
    }
    throw Exception('Login failed');
  }

  static Future<List<Product>> getProducts({String? categoryId, String? search}) async {
    final headers = await _headers();
    var query = 'store_id=${await _getStoreId()}';
    if (categoryId != null) query += '&category_id=$categoryId';
    if (search != null) query += '&search=$search';
    
    final response = await http.get(Uri.parse('$baseUrl/products?$query'), headers: headers);
    if (response.statusCode == 200) {
      final List data = jsonDecode(response.body);
      return data.map((e) => Product.fromJson(e)).toList();
    }
    throw Exception('Failed to load products');
  }

  static Future<List<Stock>> getStock() async {
    final headers = await _headers();
    final storeId = await _getStoreId();
    final response = await http.get(Uri.parse('$baseUrl/stock?store_id=$storeId'), headers: headers);
    if (response.statusCode == 200) {
      final List data = jsonDecode(response.body);
      return data.map((e) => Stock.fromJson(e)).toList();
    }
    throw Exception('Failed to load stock');
  }

  static Future<List<Customer>> getCustomers({String? search}) async {
    final headers = await _headers();
    var query = '';
    if (search != null) query = '?search=$search';
    
    final response = await http.get(Uri.parse('$baseUrl/customers$query'), headers: headers);
    if (response.statusCode == 200) {
      final List data = jsonDecode(response.body);
      return data.map((e) => Customer.fromJson(e)).toList();
    }
    throw Exception('Failed to load customers');
  }

  static Future<List<Invoice>> getInvoices({String? startDate, String? endDate}) async {
    final headers = await _headers();
    var query = 'store_id=${await _getStoreId()}';
    if (startDate != null) query += '&start_date=$startDate';
    if (endDate != null) query += '&end_date=$endDate';
    
    final response = await http.get(Uri.parse('$baseUrl/invoices?$query'), headers: headers);
    if (response.statusCode == 200) {
      final List data = jsonDecode(response.body);
      return data.map((e) => Invoice.fromJson(e)).toList();
    }
    throw Exception('Failed to load invoices');
  }

  static Future<List<Map<String, dynamic>>> getLowStock() async {
    final headers = await _headers();
    final storeId = await _getStoreId();
    final response = await http.get(Uri.parse('$baseUrl/stock/low?store_id=$storeId'), headers: headers);
    if (response.statusCode == 200) {
      return List<Map<String, dynamic>>.from(jsonDecode(response.body));
    }
    throw Exception('Failed to load low stock');
  }

  static Future<Map<String, dynamic>> getDashboard({String range = 'today'}) async {
    final headers = await _headers();
    final storeId = await _getStoreId();
    final response = await http.get(
      Uri.parse('$baseUrl/analytics/dashboard?store_id=$storeId&range=$range'),
      headers: headers,
    );
    if (response.statusCode == 200) {
      return jsonDecode(response.body);
    }
    throw Exception('Failed to load dashboard');
  }

  static Future<void> logout() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.clear();
  }

  static Future<String> _getStoreId() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getString('storeId') ?? '';
  }
}