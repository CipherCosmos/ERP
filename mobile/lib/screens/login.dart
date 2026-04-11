import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../services/api.dart';
import 'home.dart';

class LoginScreen extends StatefulWidget {
  @override
  _LoginScreenState createState() => _LoginScreenState();
}

class _LoginScreenState extends State<LoginScreen> {
  final _usernameController = TextEditingController();
  final _passwordController = TextEditingController();
  bool _loading = false;
  String? _error;

  @override
  void dispose() {
    _usernameController.dispose();
    _passwordController.dispose();
    super.dispose();
  }

  Future<void> _login() async {
    setState(() {
      _loading = true;
      _error = null;
    });

    try {
      await ApiService.login(_usernameController.text, _passwordController.text);
      if (mounted) {
        Navigator.of(context).pushReplacement(
          MaterialPageRoute(builder: (_) => HomeScreen()),
        );
      }
    } catch (e) {
      setState(() {
        _error = 'Invalid credentials';
      });
    } finally {
      setState(() {
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Padding(
        padding: EdgeInsets.all(24),
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(
              'GBOS',
              style: TextStyle(fontSize: 32, fontWeight: FontWeight.bold, color: Color(0xFF1976d2)),
            ),
            SizedBox(height: 8),
            Text('Grocery Business Operating System', style: TextStyle(color: Colors.grey[600])),
            SizedBox(height: 32),
            TextField(
              controller: _usernameController,
              decoration: InputDecoration(
                labelText: 'Username',
                border: OutlineInputBorder(),
              ),
            ),
            SizedBox(height: 16),
            TextField(
              controller: _passwordController,
              obscureText: true,
              decoration: InputDecoration(
                labelText: 'Password',
                border: OutlineInputBorder(),
              ),
            ),
            if (_error != null) ...[
              SizedBox(height: 16),
              Text(_error!, style: TextStyle(color: Colors.red)),
            ],
            SizedBox(height: 24),
            SizedBox(
              width: double.infinity,
              height: 48,
              child: ElevatedButton(
                onPressed: _loading ? null : _login,
                child: _loading ? CircularProgressIndicator(color: Colors.white) : Text('Login'),
              ),
            ),
          ],
        ),
      ),
    );
  }
}