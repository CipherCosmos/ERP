import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'screens/login.dart';
import 'screens/home.dart';

void main() {
  runApp(GBOSApp());
}

class GBOSApp extends StatelessWidget {
  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'GBOS',
      theme: ThemeData(
        primarySwatch: Colors.blue,
        useMaterial3: true,
      ),
      home: FutureBuilder<bool>(
        future: _checkAuth(),
        builder: (context, snapshot) {
          if (!snapshot.hasData) return Scaffold(body: Center(child: CircularProgressIndicator()));
          return snapshot.data! ? HomeScreen() : LoginScreen();
        },
      ),
    );
  }

  Future<bool> _checkAuth() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getString('token') != null;
  }
}