from django.conf import settings
from django.db import migrations, models
import django.db.models.deletion


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0009_sltplevel'),
        migrations.swappable_dependency(settings.AUTH_USER_MODEL),
    ]

    operations = [
        migrations.CreateModel(
            name='OptionPosition',
            fields=[
                ('id', models.BigAutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('contract_type', models.CharField(choices=[('CALL', 'Call'), ('PUT', 'Put')], max_length=4)),
                ('strike', models.DecimalField(decimal_places=4, max_digits=15)),
                ('expiry', models.DateField()),
                ('contracts', models.IntegerField()),
                ('premium_paid', models.DecimalField(decimal_places=4, max_digits=15)),
                ('status', models.CharField(
                    choices=[('OPEN', 'Open'), ('CLOSED', 'Closed'), ('EXERCISED', 'Exercised'), ('EXPIRED', 'Expired')],
                    default='OPEN', max_length=10,
                )),
                ('opened_at', models.DateTimeField(auto_now_add=True)),
                ('closed_at', models.DateTimeField(blank=True, null=True)),
                ('close_premium', models.DecimalField(blank=True, decimal_places=4, max_digits=15, null=True)),
                ('pnl', models.DecimalField(blank=True, decimal_places=4, max_digits=15, null=True)),
                ('stock', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='trading.stock')),
                ('user', models.ForeignKey(
                    on_delete=django.db.models.deletion.CASCADE,
                    related_name='option_positions',
                    to=settings.AUTH_USER_MODEL,
                )),
            ],
            options={
                'indexes': [
                    models.Index(fields=['user', 'status'], name='opt_user_status_idx'),
                ],
            },
        ),
    ]
