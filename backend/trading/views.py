from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes, action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.exceptions import ValidationError
from django.utils import timezone
from django.utils.dateparse import parse_datetime
from datetime import timedelta
from decimal import Decimal
from .models import Order, Wallet, Position, Stock
from .serializers import OrderSerializer

# 1. ניהול הזמנות (יצירה, קריאה, עדכון)
class OrderViewSet(viewsets.ModelViewSet):
    serializer_class = OrderSerializer
    permission_classes = [IsAuthenticated]

    def get_queryset(self):
        # אם זה ה-Node (מנהל), תן לו לראות את כל ההזמנות כדי שיוכל לבצע אותן
        if self.request.user.is_staff:
            return Order.objects.all()
        # אם זה משתמש רגיל, תראה לו רק את ההזמנות שלו
        return Order.objects.filter(user=self.request.user)

    def create(self, request, *args, **kwargs):
        serializer = self.get_serializer(data=request.data)
        serializer.is_valid(raise_exception=True)

        stock = serializer.validated_data['stock']
        order_type = serializer.validated_data['order_type']
        quantity = serializer.validated_data['quantity']

        # TODO: פלסטר - יוחלף בעתיד בקריאה לאורקל
        mock_oracle_price = Decimal('100.00') 

        if order_type == 'BUY':
            wallet = Wallet.objects.get(user=request.user)
            total_cost = quantity * mock_oracle_price
            
            if wallet.balance < total_cost:
                raise ValidationError({"error": "אין מספיק דולרים בארנק לביצוע הקנייה."})

        elif order_type == 'SELL':
            try:
                position = Position.objects.get(user=request.user, stock=stock)
                if position.quantity < quantity:
                    raise ValidationError({"error": "אין לך מספיק מניות למכירה."})
            except Position.DoesNotExist:
                raise ValidationError({"error": "אין לך החזקות במניה זו כלל."})

        return super().create(request, *args, **kwargs)

    def perform_create(self, serializer):
        serializer.save(user=self.request.user, status='SUBMITTED')

    # --- הפונקציה שה-Nodes מפעילים בסוף קונצנזוס ---
    @action(detail=True, methods=['post'])
    def execute_order(self, request, pk=None):
        order = self.get_object()

        if order.status != 'SUBMITTED':
            return Response({"error": "ניתן לבצע רק הזמנות שממתינות לאישור."}, status=status.HTTP_400_BAD_REQUEST)

        execution_price = request.data.get('execution_price')
        oracle_timestamp = request.data.get('timestamp') # חותמת הזמן שה-Nodes מעבירים לנו

        if not execution_price or not oracle_timestamp:
            return Response({"error": "חובה לספק מחיר ביצוע וחותמת זמן (timestamp) מה-Oracle."}, status=status.HTTP_400_BAD_REQUEST)

        execution_price = Decimal(execution_price)
        
        # --- השלמה 2: בדיקת טריות המחיר (Stale Data) ---
        oracle_time = parse_datetime(oracle_timestamp)
        if not oracle_time:
            return Response({"error": "פורמט חותמת זמן לא תקין."}, status=status.HTTP_400_BAD_REQUEST)
            
        # אם עברו יותר מ-30 שניות, דוחים את העסקה
        if timezone.now() - oracle_time > timedelta(seconds=30):
            order.status = 'REJECTED'
            order.save()
            return Response({"error": "Stale Data: המחיר מיושן ולכן העסקה בוטלה."}, status=status.HTTP_400_BAD_REQUEST)

        # --- השלמה 3: בדיקת מחיר גבול (Limit Price) ---
        if order.limit_price:
            if order.order_type == 'BUY' and execution_price > order.limit_price:
                order.status = 'REJECTED'
                order.save()
                return Response({"error": f"המחיר בשוק ({execution_price}) גבוה ממחיר הגבול ({order.limit_price}). העסקה בוטלה."}, status=status.HTTP_400_BAD_REQUEST)
            elif order.order_type == 'SELL' and execution_price < order.limit_price:
                order.status = 'REJECTED'
                order.save()
                return Response({"error": f"המחיר בשוק ({execution_price}) נמוך ממחיר הגבול ({order.limit_price}). העסקה בוטלה."}, status=status.HTTP_400_BAD_REQUEST)

        # ---- שלב ביצוע העסקה בפועל ----
        total_value = order.quantity * execution_price
        wallet = order.user.wallet

        if order.order_type == 'BUY':
            # בדיקת יתרה סופית לפי המחיר האמיתי
            if wallet.balance < total_value:
                order.status = 'REJECTED'
                order.save()
                return Response({"error": "אין מספיק דולרים לביצוע במחיר השוק הנוכחי."}, status=status.HTTP_400_BAD_REQUEST)
                
            wallet.balance -= total_value
            position, created = Position.objects.get_or_create(user=order.user, stock=order.stock)
            position.quantity += order.quantity
            position.save()

        elif order.order_type == 'SELL':
            wallet.balance += total_value
            position = Position.objects.get(user=order.user, stock=order.stock)
            if position.quantity < order.quantity:
                order.status = 'REJECTED'
                order.save()
                return Response({"error": "לא ניתן למכור יותר מהמלאי הקיים."}, status=status.HTTP_400_BAD_REQUEST)
            position.quantity -= order.quantity
            if position.quantity == 0:
                position.delete()
            else:
                position.save()

        wallet.save()
        
        # עדכון ההזמנה ושמירה במסד
        order.execution_price = execution_price
        order.status = 'CONFIRMED'
        order.save()

        return Response({
            "status": "העסקה בוצעה בהצלחה והיתרות עודכנו!", 
            "execution_price": execution_price
        })

# 2. חישוב והצגת תיק ההשקעות (Portfolio)
@api_view(['GET'])
@permission_classes([IsAuthenticated])
def portfolio_view(request):
    wallet = Wallet.objects.get(user=request.user)
    positions = Position.objects.filter(user=request.user)
    
    holdings = []
    for pos in positions:
        holdings.append({
            "ticker": pos.stock.ticker,
            "quantity": str(pos.quantity)
        })
    
    return Response({
        "usd_balance": str(wallet.balance),
        "holdings": holdings
    })
