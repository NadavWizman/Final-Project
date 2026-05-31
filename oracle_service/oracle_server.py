from flask import Flask, jsonify
import yfinance as yf
from datetime import datetime, timezone

app = Flask(__name__)

@app.route('/price/<ticker>')
def get_price(ticker):
    try:
        stock = yf.Ticker(ticker)
        data = stock.history(period='1d')

        if data.empty:
            return jsonify({"error": f"No data found for {ticker}"}), 404

        price = round(float(data['Close'].iloc[-1]), 2)
        timestamp = datetime.now(timezone.utc).isoformat()

        return jsonify({
            "ticker": ticker,
            "execution_price": str(price),
            "timestamp": timestamp
        })
    except Exception as e:
        return jsonify({"error": str(e)}), 500

if __name__ == '__main__':
    print("Oracle Service running on port 8001")
    app.run(port=8001)
