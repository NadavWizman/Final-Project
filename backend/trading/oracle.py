import yfinance as yf
from datetime import datetime, timezone
import json

def get_stock_data(ticker_symbol):
    """
    Queries Yahoo Finance for the current price of a stock and returns it
    together with a precise UTC timestamp.
    """
    try:
        # create a ticker object
        stock = yf.Ticker(ticker_symbol)

        # fetch today's price history
        todays_data = stock.history(period='1d')

        if todays_data.empty:
            return {"error": f"No data found for ticker {ticker_symbol}"}

        # take the most recent close/current price
        current_price = round(todays_data['Close'].iloc[-1], 2)

        # build an ISO timestamp (the format Django expects)
        current_timestamp = datetime.now(timezone.utc).isoformat()

        # return the data as a dict (ready for JSON serialization)
        return {
            "ticker": ticker_symbol,
            "execution_price": str(current_price),
            "timestamp": current_timestamp
        }

    except Exception as e:
        return {"error": str(e)}

# --- local testing ---
if __name__ == "__main__":
    print("Connecting to oracle and fetching data for Apple (AAPL)...")
    result = get_stock_data("AAPL")

    # pretty-print the result
    print(json.dumps(result, indent=4))
